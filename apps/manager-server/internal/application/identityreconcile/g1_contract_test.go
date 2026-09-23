package identityreconcile_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/cpaidentityinventory"
	adaptersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identitystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityreconcile"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityinventory"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

const (
	g1ContractID = "cpamp-v2-phase3-g1-stable-identity-v1"
	g1Baseline   = "b3bd5c8808ab174c6de295cb1f9113e472054b51"
	g1Runtime    = "trusted-runtime-R"
	g1AuthID     = "cpa-auth-A"
)

type g1Contract struct {
	Schema            string       `json:"$schema"`
	SchemaVersion     int          `json:"schemaVersion"`
	ContractID        string       `json:"contractId"`
	ExecutionBaseline string       `json:"executionBaseline"`
	Scenarios         []g1Scenario `json:"scenarios"`
}

type g1Scenario struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	OldHash    string   `json:"oldHash,omitempty"`
	NewHash    string   `json:"newHash,omitempty"`
	Assertions []string `json:"assertions"`
}

var g1Mandatory = map[string]string{
	"g1-canonical-id-independent-source":          "canonical_source",
	"g1-credential-continuity-reload-restart":     "credential_continuity",
	"g1-generation-not-business-revision":         "generation_revision",
	"g1-passive-replacement-vs-explicit-rotation": "replacement_rotation",
	"g1-superseded-id-non-reuse":                  "superseded_nonreuse",
	"g1-unsupported-ambiguous-fail-closed":        "fail_closed",
	"g1-external-no-trusted-identity":             "external_boundary",
}

// These are the minimum business claims each mandatory scenario must execute.
// The checked-in record selects their execution and the assertion loop checks the result.
var g1Claims = map[string][]string{
	"canonical_source":      {"canonical_id_not_source", "binding_current"},
	"credential_continuity": {"same_canonical_id", "revision_unchanged", "binding_current", "metadata_refreshed", "generation_refreshed"},
	"generation_revision":   {"same_canonical_id", "revision_unchanged", "revision_incremented_once", "lifecycle_missing", "lifecycle_active", "generation_refreshed"},
	"replacement_rotation":  {"different_canonical_id", "same_canonical_id", "revision_incremented_once", "lifecycle_missing", "lifecycle_active", "binding_retired", "binding_current"},
	"superseded_nonreuse":   {"different_canonical_id", "lifecycle_superseded", "binding_retired", "binding_current"},
	"fail_closed":           {"inventory_rejected", "whole_snapshot_discarded", "zero_canonical_writes"},
	"external_boundary":     {"trusted_runtime_identity_absent", "zero_canonical_writes"},
}

func g1LoadContract(t *testing.T) g1Contract {
	t.Helper()
	// Go tests run from this package directory. Open the checked-in repository fixture.
	fixturePath := filepath.Join("..", "..", "..", "..", "..", "tests", "fixtures", "phase3-g1", "contract.json")
	f, err := os.Open(fixturePath)
	if err != nil {
		t.Fatalf("open checked-in G1 contract %s: %v", fixturePath, err)
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	var contract g1Contract
	if err := decoder.Decode(&contract); err != nil {
		t.Fatalf("decode G1 contract: %v", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Fatalf("G1 contract has trailing JSON: %v", err)
	}
	if contract.Schema != "./contract.schema.json" || contract.SchemaVersion != 1 ||
		contract.ContractID != g1ContractID || contract.ExecutionBaseline != g1Baseline {
		t.Fatalf("G1 contract header invalid: %+v", contract)
	}
	if len(contract.Scenarios) != len(g1Mandatory) {
		t.Fatalf("G1 contract has %d scenarios, want %d", len(contract.Scenarios), len(g1Mandatory))
	}
	seen := make(map[string]bool)
	for _, scenario := range contract.Scenarios {
		kind, known := g1Mandatory[scenario.ID]
		if !known || seen[scenario.ID] || scenario.Kind != kind {
			t.Fatalf("unknown, duplicate, or mismatched G1 scenario: %+v", scenario)
		}
		seen[scenario.ID] = true
		if scenario.Kind == "replacement_rotation" {
			if !identity.IsValidSHA256Hex(scenario.OldHash) || !identity.IsValidSHA256Hex(scenario.NewHash) || scenario.OldHash == scenario.NewHash {
				t.Fatalf("replacement hashes must be distinct normalized SHA-256 hex: %+v", scenario)
			}
		} else if scenario.OldHash != "" || scenario.NewHash != "" {
			t.Fatalf("unexpected source hashes in %s", scenario.ID)
		}
		claims := make(map[string]bool)
		for _, assertion := range scenario.Assertions {
			allowed := false
			for _, required := range g1Claims[kind] {
				if assertion == required {
					allowed = true
					break
				}
			}
			if !allowed || claims[assertion] {
				t.Fatalf("unknown or duplicate assertion %q in %s", assertion, scenario.ID)
			}
			claims[assertion] = true
		}
		if len(claims) != len(g1Claims[kind]) {
			t.Fatalf("%s must execute all mandatory %s claims: %v", scenario.ID, kind, scenario.Assertions)
		}
	}
	return contract
}

func TestG1CheckedInContract(t *testing.T) {
	contract := g1LoadContract(t)
	for _, scenario := range contract.Scenarios {
		t.Run(scenario.ID, func(t *testing.T) {
			var observed map[string]bool
			switch scenario.Kind {
			case "canonical_source":
				observed = g1CanonicalSource(t)
			case "credential_continuity":
				observed = g1CredentialContinuity(t)
			case "generation_revision":
				observed = g1GenerationRevision(t)
			case "replacement_rotation":
				observed = g1ReplacementRotation(t, scenario.OldHash, scenario.NewHash)
			case "superseded_nonreuse":
				observed = g1SupersededNonreuse(t)
			case "fail_closed":
				observed = g1FailClosed(t)
			case "external_boundary":
				observed = g1ExternalBoundary(t)
			default:
				t.Fatalf("unknown scenario kind %q", scenario.Kind)
			}
			for _, assertion := range scenario.Assertions {
				if !observed[assertion] {
					t.Errorf("G1 assertion %q failed", assertion)
				}
			}
		})
	}
}

func g1OpenDB(t *testing.T) (*sql.DB, ports.Repository) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "g1.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, adaptersqlite.New(db)
}

func g1Snapshot(t *testing.T, repo ports.Repository, generation uint64, at int64, hashes []string, credentials []ports.CredentialSnapshotItem) ports.ReconcileSnapshotResult {
	t.Helper()
	items := make([]ports.APIKeySnapshotItem, len(hashes))
	for i, hash := range hashes {
		items[i] = ports.APIKeySnapshotItem{APIKeyHash: hash}
	}
	result, err := repo.ApplyPassiveSnapshot(t.Context(), ports.ReconcileSnapshotParams{
		RuntimeIdentity: g1Runtime, ObservedRuntimeGeneration: generation,
		APIKeys: items, Credentials: credentials, NowMS: at,
	})
	if err != nil {
		t.Fatalf("apply G1 passive snapshot: %v", err)
	}
	return result
}

func g1Key(t *testing.T, repo ports.Repository, hash string) (identity.APIKeyIdentity, identity.APIKeySourceBinding) {
	t.Helper()
	ent, binding, err := repo.FindActiveAPIKeyBySource(t.Context(), g1Runtime, hash)
	if err != nil {
		t.Fatalf("find G1 API key: %v", err)
	}
	return ent, binding
}

func g1Credential(t *testing.T, repo ports.Repository, source string) (identity.CredentialIdentity, identity.CredentialSourceBinding) {
	t.Helper()
	ent, binding, err := repo.FindActiveCredentialBySource(t.Context(), g1Runtime, source)
	if err != nil {
		t.Fatalf("find G1 credential: %v", err)
	}
	return ent, binding
}

func g1CanonicalSource(t *testing.T) map[string]bool {
	_, repo := g1OpenDB(t)
	hash := strings.Repeat("c", 64)
	g1Snapshot(t, repo, 5, 1000, []string{hash}, []ports.CredentialSnapshotItem{{SourceAuthID: g1AuthID, PhysicalName: "a.json"}})
	key, keyBinding := g1Key(t, repo, hash)
	credential, credentialBinding := g1Credential(t, repo, g1AuthID)
	return map[string]bool{
		"canonical_id_not_source": key.ID.Validate() == nil && credential.ID.Validate() == nil && string(key.ID) != hash && string(credential.ID) != g1AuthID,
		"binding_current":         keyBinding.RetiredAtMS == 0 && credentialBinding.RetiredAtMS == 0,
	}
}

func g1Reconcile(t *testing.T, repo ports.Repository, generation model.RuntimeGeneration, at int64, credentials []identityinventory.CredentialObservation) {
	t.Helper()
	status := model.RuntimeObservedStatus{Identity: g1Runtime, Generation: generation, State: model.RuntimeStateReady}
	observer := &sequenceRuntimeObserver{statuses: []func() (model.RuntimeObservedStatus, error){
		func() (model.RuntimeObservedStatus, error) { return status, nil },
		func() (model.RuntimeObservedStatus, error) { return status, nil },
	}}
	svc, err := identityreconcile.NewService(identityreconcile.Config{
		RuntimeObserver: observer, ConnectionResolver: staticConnectionResolver("http://cpa.invalid", "management-key"),
		InventoryClient: &fakeInventoryClient{creds: credentials}, IdentityRepo: repo, TimeSource: func() int64 { return at },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReconcileOnce(t.Context()); err != nil {
		t.Fatalf("G1 reconciliation: %v", err)
	}
}

func g1CredentialContinuity(t *testing.T) map[string]bool {
	path := filepath.Join(t.TempDir(), "continuity.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	repo := adaptersqlite.New(db)
	initial := identityinventory.CredentialObservation{
		SourceAuthID: g1AuthID, PhysicalName: "original.json", AuthIndex: "1", Provider: "codex",
		AccountSnapshot: "old@example.test", AccountIDSnapshot: "account-old",
	}
	changed := identityinventory.CredentialObservation{
		SourceAuthID: g1AuthID, PhysicalName: "renamed.json", AuthIndex: "9", Provider: "updated",
		AccountSnapshot: "new@example.test", AccountIDSnapshot: "account-new",
	}
	g1Reconcile(t, repo, 91, 1000, []identityinventory.CredentialObservation{initial})
	before, _ := g1Credential(t, repo, g1AuthID)
	g1Reconcile(t, repo, 91, 2000, []identityinventory.CredentialObservation{changed})
	metadataIdentity, metadataBinding := g1Credential(t, repo, g1AuthID)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo = adaptersqlite.New(db)
	// Generation is opaque provenance: the restarted value can be smaller.
	g1Reconcile(t, repo, 7, 3000, []identityinventory.CredentialObservation{changed})
	after, binding := g1Credential(t, repo, g1AuthID)
	return map[string]bool{
		"same_canonical_id":    before.ID == metadataIdentity.ID && before.ID == after.ID,
		"revision_unchanged":   before.Revision == metadataIdentity.Revision && before.Revision == after.Revision,
		"binding_current":      binding.RetiredAtMS == 0 && metadataBinding.RetiredAtMS == 0,
		"metadata_refreshed":   binding.PhysicalName == changed.PhysicalName && binding.AuthIndex == changed.AuthIndex && binding.Provider == changed.Provider && binding.AccountSnapshot == changed.AccountSnapshot && binding.AccountIDSnapshot == changed.AccountIDSnapshot,
		"generation_refreshed": binding.ObservedRuntimeGeneration == 7 && metadataBinding.ObservedRuntimeGeneration == 91,
	}
}

func g1GenerationRevision(t *testing.T) map[string]bool {
	_, repo := g1OpenDB(t)
	hash := strings.Repeat("d", 64)
	credential := []ports.CredentialSnapshotItem{{SourceAuthID: g1AuthID, PhysicalName: "a.json"}}
	g1Snapshot(t, repo, 800, 1000, []string{hash}, credential)
	firstKey, _ := g1Key(t, repo, hash)
	firstCredential, _ := g1Credential(t, repo, g1AuthID)
	g1Snapshot(t, repo, 2, 2000, []string{hash}, credential)
	secondKey, keyBinding := g1Key(t, repo, hash)
	secondCredential, credentialBinding := g1Credential(t, repo, g1AuthID)
	g1Snapshot(t, repo, 2, 3000, nil, nil)
	missingKey, err := repo.LoadAPIKeyByID(t.Context(), firstKey.ID)
	if err != nil {
		t.Fatal(err)
	}
	missingCredential, err := repo.LoadCredentialByID(t.Context(), firstCredential.ID)
	if err != nil {
		t.Fatal(err)
	}
	g1Snapshot(t, repo, 2, 4000, []string{hash}, credential)
	recoveredKey, _ := g1Key(t, repo, hash)
	recoveredCredential, _ := g1Credential(t, repo, g1AuthID)
	return map[string]bool{
		"same_canonical_id":         firstKey.ID == secondKey.ID && firstKey.ID == recoveredKey.ID && firstCredential.ID == secondCredential.ID && firstCredential.ID == recoveredCredential.ID,
		"revision_unchanged":        firstKey.Revision == secondKey.Revision && firstCredential.Revision == secondCredential.Revision,
		"revision_incremented_once": missingKey.Revision == secondKey.Revision+1 && missingCredential.Revision == secondCredential.Revision+1 && recoveredKey.Revision == missingKey.Revision+1 && recoveredCredential.Revision == missingCredential.Revision+1,
		"lifecycle_missing":         missingKey.Lifecycle == identity.LifecycleMissing && missingCredential.Lifecycle == identity.LifecycleMissing,
		"lifecycle_active":          recoveredKey.Lifecycle == identity.LifecycleActive && recoveredCredential.Lifecycle == identity.LifecycleActive,
		"generation_refreshed":      keyBinding.ObservedRuntimeGeneration == 2 && credentialBinding.ObservedRuntimeGeneration == 2,
	}
}

func g1RetiredBinding(t *testing.T, db *sql.DB, table, idColumn, id string) bool {
	t.Helper()
	var count int
	query := fmt.Sprintf("select count(*) from %s where %s = ? and retired_at_ms is not null", table, idColumn)
	if err := db.QueryRowContext(t.Context(), query, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count > 0
}

func g1ReplacementRotation(t *testing.T, oldHash, newHash string) map[string]bool {
	_, passiveRepo := g1OpenDB(t)
	g1Snapshot(t, passiveRepo, 1, 1000, []string{oldHash}, nil)
	passiveOld, _ := g1Key(t, passiveRepo, oldHash)
	g1Snapshot(t, passiveRepo, 1, 2000, []string{newHash}, nil)
	passiveNew, passiveBinding := g1Key(t, passiveRepo, newHash)
	passiveOldAfter, err := passiveRepo.LoadAPIKeyByID(t.Context(), passiveOld.ID)
	if err != nil {
		t.Fatal(err)
	}
	rotateDB, rotateRepo := g1OpenDB(t)
	g1Snapshot(t, rotateRepo, 1, 1000, []string{oldHash}, nil)
	rotateOld, _ := g1Key(t, rotateRepo, oldHash)
	mutations := rotateRepo.(ports.MutationRepository)
	intent, err := mutations.PrepareAPIKeyMutation(t.Context(), ports.PrepareAPIKeyMutationParams{
		Kind: ports.APIKeyMutationRotate, RuntimeIdentity: g1Runtime, ObservedRuntimeGeneration: 1,
		Evidence:      ports.APIKeyMutationEvidence{OldHash: oldHash, NewHash: newHash, ExactOldCount: 1, NormalizedOldCount: 1},
		OwnerInstance: "g1-process", NowMS: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mutations.MarkAPIKeyMutationForwardComplete(t.Context(), intent, "g1-process", 2001); err != nil {
		t.Fatal(err)
	}
	outcome, err := mutations.ResolveAPIKeyMutation(t.Context(), ports.ResolveAPIKeyMutationParams{
		IntentID: intent, RuntimeIdentity: g1Runtime, ObservedRuntimeGeneration: 3,
		ObservedHashes: []string{newHash}, NowMS: 2002,
	})
	if err != nil || outcome != ports.APIKeyMutationSuccess {
		t.Fatalf("explicit rotation outcome %q: %v", outcome, err)
	}
	rotated, rotateBinding := g1Key(t, rotateRepo, newHash)
	_, _, oldActiveErr := rotateRepo.FindActiveAPIKeyBySource(t.Context(), g1Runtime, oldHash)
	// The retired binding belongs to the same canonical identity after rotation.
	return map[string]bool{
		"different_canonical_id":    passiveOld.ID != passiveNew.ID,
		"same_canonical_id":         rotateOld.ID == rotated.ID,
		"revision_incremented_once": rotated.Revision == rotateOld.Revision+1,
		"lifecycle_missing":         passiveOldAfter.Lifecycle == identity.LifecycleMissing,
		"lifecycle_active":          rotated.Lifecycle == identity.LifecycleActive,
		"binding_retired":           errors.Is(oldActiveErr, ports.ErrNotFound) && g1RetiredBinding(t, rotateDB, sqlite.GatewayAPIKeySourceBindingsTable, "api_key_id", string(rotateOld.ID)),
		"binding_current":           passiveBinding.RetiredAtMS == 0 && rotateBinding.RetiredAtMS == 0,
	}
}

func g1SupersededNonreuse(t *testing.T) map[string]bool {
	apiDB, apiRepo := g1OpenDB(t)
	hash := strings.Repeat("e", 64)
	g1Snapshot(t, apiRepo, 1, 1000, []string{hash}, nil)
	oldKey, _ := g1Key(t, apiRepo, hash)
	apiMutations := apiRepo.(ports.MutationRepository)
	apiIntent, err := apiMutations.PrepareAPIKeyMutation(t.Context(), ports.PrepareAPIKeyMutationParams{
		Kind: ports.APIKeyMutationDelete, RuntimeIdentity: g1Runtime, ObservedRuntimeGeneration: 1,
		Evidence:      ports.APIKeyMutationEvidence{OldHash: hash, NormalizedOldCount: 1},
		OwnerInstance: "g1-process", NowMS: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := apiMutations.MarkAPIKeyMutationForwardComplete(t.Context(), apiIntent, "g1-process", 2001); err != nil {
		t.Fatal(err)
	}
	apiOutcome, err := apiMutations.ResolveAPIKeyMutation(t.Context(), ports.ResolveAPIKeyMutationParams{
		IntentID: apiIntent, RuntimeIdentity: g1Runtime, ObservedRuntimeGeneration: 1,
		ObservedHashes: nil, NowMS: 2002,
	})
	if err != nil || apiOutcome != ports.APIKeyMutationSuccess {
		t.Fatalf("explicit API-key delete outcome %q: %v", apiOutcome, err)
	}
	supersededKey, err := apiRepo.LoadAPIKeyByID(t.Context(), oldKey.ID)
	if err != nil {
		t.Fatal(err)
	}
	apiRetired := g1RetiredBinding(t, apiDB, sqlite.GatewayAPIKeySourceBindingsTable, "api_key_id", string(oldKey.ID))
	g1Snapshot(t, apiRepo, 2, 3000, []string{hash}, nil)
	newKey, newKeyBinding := g1Key(t, apiRepo, hash)
	oldKeyAgain, err := apiRepo.LoadAPIKeyByID(t.Context(), oldKey.ID)
	if err != nil {
		t.Fatal(err)
	}

	credentialDB, credentialRepo := g1OpenDB(t)
	credentialItem := []ports.CredentialSnapshotItem{{SourceAuthID: g1AuthID, PhysicalName: "credential.json", Provider: "codex"}}
	g1Snapshot(t, credentialRepo, 1, 1000, nil, credentialItem)
	oldCredential, _ := g1Credential(t, credentialRepo, g1AuthID)
	credentialDeletes := credentialRepo.(ports.CredentialDeleteRepository)
	credentialIntent, err := credentialDeletes.PrepareCredentialDelete(t.Context(), ports.PrepareCredentialDeleteParams{
		RuntimeIdentity: g1Runtime, ObservedRuntimeGeneration: 1, PhysicalName: "credential.json",
		SourceAuthIDs: []string{g1AuthID}, OwnerInstance: "g1-process", NowMS: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := credentialDeletes.MarkCredentialDeleteForwardComplete(t.Context(), credentialIntent, "g1-process", 2001); err != nil {
		t.Fatal(err)
	}
	credentialOutcome, err := credentialDeletes.ResolveCredentialDelete(t.Context(), ports.ResolveCredentialDeleteParams{
		IntentID: credentialIntent, RuntimeIdentity: g1Runtime, ObservedRuntimeGeneration: 1,
		ObservedSourceAuthIDs: nil, PhysicalEvidence: ports.PhysicalSourceAbsent, NowMS: 2002,
	})
	if err != nil || credentialOutcome != ports.CredentialDeleteSuccess {
		t.Fatalf("explicit credential delete outcome %q: %v", credentialOutcome, err)
	}
	supersededCredential, err := credentialRepo.LoadCredentialByID(t.Context(), oldCredential.ID)
	if err != nil {
		t.Fatal(err)
	}
	credentialRetired := g1RetiredBinding(t, credentialDB, sqlite.GatewayCredentialSourceBindingsTable, "credential_id", string(oldCredential.ID))
	g1Snapshot(t, credentialRepo, 2, 3000, nil, credentialItem)
	newCredential, newCredentialBinding := g1Credential(t, credentialRepo, g1AuthID)
	oldCredentialAgain, err := credentialRepo.LoadCredentialByID(t.Context(), oldCredential.ID)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]bool{
		"different_canonical_id": oldKey.ID != newKey.ID && oldCredential.ID != newCredential.ID,
		"lifecycle_superseded":   supersededKey.Lifecycle == identity.LifecycleSuperseded && oldKeyAgain.Lifecycle == identity.LifecycleSuperseded && supersededCredential.Lifecycle == identity.LifecycleSuperseded && oldCredentialAgain.Lifecycle == identity.LifecycleSuperseded,
		"binding_retired":        apiRetired && credentialRetired,
		"binding_current":        newKeyBinding.RetiredAtMS == 0 && newCredentialBinding.RetiredAtMS == 0,
	}
}

type g1CountingRepo struct {
	ports.Repository
	applyCalls int
}

func (r *g1CountingRepo) ApplyPassiveSnapshot(ctx context.Context, params ports.ReconcileSnapshotParams) (ports.ReconcileSnapshotResult, error) {
	r.applyCalls++
	return r.Repository.ApplyPassiveSnapshot(ctx, params)
}

func g1SeededGuard(t *testing.T) (*sql.DB, *g1CountingRepo, identity.APIKeyIdentity, identity.CredentialIdentity) {
	t.Helper()
	db, repo := g1OpenDB(t)
	hash := strings.Repeat("f", 64)
	g1Snapshot(t, repo, 1, 1000, []string{hash}, []ports.CredentialSnapshotItem{{SourceAuthID: g1AuthID, PhysicalName: "existing.json"}})
	key, _ := g1Key(t, repo, hash)
	credential, _ := g1Credential(t, repo, g1AuthID)
	return db, &g1CountingRepo{Repository: repo}, key, credential
}

func g1Unchanged(t *testing.T, db *sql.DB, repo *g1CountingRepo, key identity.APIKeyIdentity, credential identity.CredentialIdentity) bool {
	t.Helper()
	afterKey, err := repo.LoadAPIKeyByID(t.Context(), key.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterCredential, err := repo.LoadCredentialByID(t.Context(), credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	var keyCount, credentialCount int
	if err := db.QueryRowContext(t.Context(), "select count(*) from "+sqlite.GatewayAPIKeyIdentitiesTable).Scan(&keyCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(t.Context(), "select count(*) from "+sqlite.GatewayCredentialIdentitiesTable).Scan(&credentialCount); err != nil {
		t.Fatal(err)
	}
	return repo.applyCalls == 0 && keyCount == 1 && credentialCount == 1 &&
		afterKey == key && afterCredential == credential
}

func g1RunGuarded(t *testing.T, repo *g1CountingRepo, observer identityreconcile.RuntimeObserver, inventory identityinventory.Client, baseURL string) error {
	t.Helper()
	svc, err := identityreconcile.NewService(identityreconcile.Config{
		RuntimeObserver: observer, ConnectionResolver: staticConnectionResolver(baseURL, "management-key"),
		InventoryClient: inventory, IdentityRepo: repo, TimeSource: func() int64 { return 2000 },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.ReconcileOnce(t.Context())
	return err
}

func g1ReadySequence(preGeneration, postGeneration model.RuntimeGeneration) *sequenceRuntimeObserver {
	pre := model.RuntimeObservedStatus{Identity: g1Runtime, Generation: preGeneration, State: model.RuntimeStateReady}
	post := model.RuntimeObservedStatus{Identity: g1Runtime, Generation: postGeneration, State: model.RuntimeStateReady}
	return &sequenceRuntimeObserver{statuses: []func() (model.RuntimeObservedStatus, error){
		func() (model.RuntimeObservedStatus, error) { return pre, nil },
		func() (model.RuntimeObservedStatus, error) { return post, nil },
	}}
}

func g1FailClosed(t *testing.T) map[string]bool {
	// A: a non-runtime-only auth file has metadata but no trusted CPA Auth.ID.
	caseADB, caseARepo, caseAKey, caseACredential := g1SeededGuard(t)
	caseAServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/management/api-keys":
			_, _ = io.WriteString(w, `{"api-keys":[]}`)
		case "/v0/management/auth-files":
			_, _ = io.WriteString(w, `{"files":[{"name":"renamed.json","auth_index":"9","provider":"codex","account":"user@example.test","account_id":"account-9","runtime_only":false}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer caseAServer.Close()
	caseAErr := g1RunGuarded(t, caseARepo, g1ReadySequence(1, 1), cpaidentityinventory.New(caseAServer.Client(), nil), caseAServer.URL)
	caseAUnchanged := g1Unchanged(t, caseADB, caseARepo, caseAKey, caseACredential)

	// B: malformed API-key authority must not become an empty inventory.
	caseBDB, caseBRepo, caseBKey, caseBCredential := g1SeededGuard(t)
	caseBServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/management/api-keys":
			_, _ = io.WriteString(w, `{"api-keys":null}`)
		case "/v0/management/auth-files":
			_, _ = io.WriteString(w, `{"files":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer caseBServer.Close()
	caseBErr := g1RunGuarded(t, caseBRepo, g1ReadySequence(1, 1), cpaidentityinventory.New(caseBServer.Client(), nil), caseBServer.URL)
	caseBUnchanged := g1Unchanged(t, caseBDB, caseBRepo, caseBKey, caseBCredential)

	// C: both inventories are valid, but the post-capture generation changed.
	caseCDB, caseCRepo, caseCKey, caseCCredential := g1SeededGuard(t)
	validInventory := &fakeInventoryClient{
		apiKeys: []identityinventory.APIKeyObservation{{KeyHash: strings.Repeat("a", 64)}},
		creds:   []identityinventory.CredentialObservation{{SourceAuthID: "new-auth", PhysicalName: "new.json"}},
	}
	caseCErr := g1RunGuarded(t, caseCRepo, g1ReadySequence(1, 2), validInventory, "http://cpa.invalid")
	caseCUnchanged := g1Unchanged(t, caseCDB, caseCRepo, caseCKey, caseCCredential)
	return map[string]bool{
		"inventory_rejected":       errors.Is(caseAErr, identityreconcile.ErrCredentialInventoryFailed) && errors.Is(caseAErr, cpaidentityinventory.ErrIncompleteCredentialInventory) && errors.Is(caseBErr, identityreconcile.ErrAPIKeyInventoryFailed) && errors.Is(caseBErr, cpaidentityinventory.ErrMalformedAPIKeysResponse),
		"whole_snapshot_discarded": errors.Is(caseCErr, identityreconcile.ErrRuntimeFenceChanged),
		"zero_canonical_writes":    caseAUnchanged && caseBUnchanged && caseCUnchanged,
	}
}

func g1ExternalBoundary(t *testing.T) map[string]bool {
	db, repo, key, credential := g1SeededGuard(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("X-CPA-Version", "v7.3.3")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	external := runtime.NewExternalClient(server.URL, "management-key")
	status, err := external.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reconcileErr := g1RunGuarded(t, repo, external, &fakeInventoryClient{}, server.URL)
	return map[string]bool{
		"trusted_runtime_identity_absent": status.State == model.RuntimeStateReady && status.CPAObservedVersion == "v7.3.3" && status.Identity == "" && status.Generation == 0 && status.ProtocolVersion == "" && status.Capabilities == nil,
		"zero_canonical_writes":           requests == 2 && errors.Is(reconcileErr, identityreconcile.ErrRuntimeObservationIncomplete) && g1Unchanged(t, db, repo, key, credential),
	}
}
