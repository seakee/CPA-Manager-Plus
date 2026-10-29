package freshstore

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

// rebindFixture is a committed Connect External installation with one protected
// credential in both persisted connection rows.
type rebindFixture struct {
	path      string
	db        *sql.DB
	store     Store
	rebinder  ConnectionRebinder
	id        bootstrap.FreshIdentity
	protector *security.Protector
	record    Record
	baseURL   string
}

// newRebindFixture commits the installation a rebind replaces a credential in.
func newRebindFixture(t *testing.T) *rebindFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manager.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := New(db)
	id := bootstrap.FreshIdentity{AdmissionID: "a", InstallationID: "i", Generation: "g", DatabasePath: path, OperationID: "fresh-operation"}
	admin, err := security.NewAdminCredential("Cpamp_Rebind_Admin_Secret_417293", "host")
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(t.Context(), id, &admin)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector([]byte("rebind durable test key"))
	if err != nil {
		t.Fatal(err)
	}
	secret, err := protector.ProtectString("management-before-rotation")
	if err != nil {
		t.Fatal(err)
	}
	const baseURL = "http://cpa:8317"
	digest := sha256.Sum256([]byte(baseURL))
	record, err = store.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: id.OperationID, Revision: 1, Target: "/console", Stage: bootstrap.PrefixCommitted, Committed: true}
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.ConnectExternal, Stage: "committed",
			TargetIdentity: "external:sha256:" + hex.EncodeToString(digest[:]), TargetGeneration: 1,
		}
		r.Operation.ProductComplete = true
		r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: baseURL, ManagementKey: secret}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rebinder, ok := store.(ConnectionRebinder)
	if !ok {
		t.Fatal("the store does not implement the rebind capability")
	}
	return &rebindFixture{path: path, db: db, store: store, rebinder: rebinder, id: id,
		protector: protector, record: record, baseURL: baseURL}
}

// seedOperatorConfig writes a manager configuration the operator already owns, so
// the rebind is shown to preserve everything but the connection leaf. The
// connection leaf is carried over: that row is where the store reads the live
// connection from, so dropping it would be an edit of the installation.
func (f *rebindFixture) seedOperatorConfig(t *testing.T) model.ManagerConfig {
	t.Helper()
	enabled := true
	cfg := model.ManagerConfig{
		CPAConnection:   *f.record.Connection,
		Collector:       model.ManagerCollectorConfig{Enabled: &enabled},
		CodexInspection: model.ManagerCodexInspectionConfig{},
		UpdatedAtMS:     7,
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(`insert into settings(key,value,updated_at_ms) values('manager_config_v1',?,1)
		on conflict(key) do update set value=excluded.value,updated_at_ms=excluded.updated_at_ms`, string(data)); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// managerConfig reads the compatibility row the rebind mirrors the connection to.
func (f *rebindFixture) managerConfig(t *testing.T) model.ManagerConfig {
	t.Helper()
	var data string
	if err := f.db.QueryRow("select value from settings where key='manager_config_v1'").Scan(&data); err != nil {
		t.Fatal(err)
	}
	var cfg model.ManagerConfig
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// setupRow reads the second compatibility mirror.
func (f *rebindFixture) setupRow(t *testing.T) model.Setup {
	t.Helper()
	var data string
	if err := f.db.QueryRow("select value from settings where key='setup'").Scan(&data); err != nil {
		t.Fatal(err)
	}
	var setup model.Setup
	if err := json.Unmarshal([]byte(data), &setup); err != nil {
		t.Fatal(err)
	}
	return setup
}

// TestRebindConnectionReplacesOnlyTheCredential proves the one connection rewrite
// this Manager performs moves nothing but the protected credential: the address,
// the installation identity, the pinned target generation and the rest of the
// operator's manager configuration survive, both compatibility mirrors carry the
// new envelope, the store reopens with it, and no plaintext reaches the settings
// table.
func TestRebindConnectionReplacesOnlyTheCredential(t *testing.T) {
	fixture := newRebindFixture(t)
	operator := fixture.seedOperatorConfig(t)
	replacement, err := fixture.protector.ProtectString("management-after-rotation")
	if err != nil {
		t.Fatal(err)
	}
	rebound, err := fixture.rebinder.RebindConnection(t.Context(), fixture.id, fixture.record.Operation.Revision,
		model.ManagerCPAConnectionConfig{CPABaseURL: fixture.baseURL, ManagementKey: replacement})
	if err != nil {
		t.Fatal(err)
	}
	if rebound.Operation.Revision != fixture.record.Operation.Revision+1 {
		t.Fatalf("revision = %d, want %d", rebound.Operation.Revision, fixture.record.Operation.Revision+1)
	}
	// The target generation names the installation, and a credential rotation is
	// not a new installation.
	if rebound.Operation.Strategy.TargetGeneration != fixture.record.Operation.Strategy.TargetGeneration ||
		rebound.Operation.Strategy.TargetIdentity != fixture.record.Operation.Strategy.TargetIdentity {
		t.Fatalf("the installation moved: %+v", rebound.Operation.Strategy)
	}
	if rebound.Connection == nil || rebound.Connection.CPABaseURL != fixture.baseURL ||
		rebound.Connection.ManagementKey != replacement {
		t.Fatalf("connection = %+v", rebound.Connection)
	}
	// Both mirrored rows a legacy reader resolves carry the new envelope.
	manager := fixture.managerConfig(t)
	if manager.CPAConnection.ManagementKey != replacement || manager.CPAConnection.CPABaseURL != fixture.baseURL {
		t.Fatalf("the manager mirror kept the replaced credential: %+v", manager.CPAConnection)
	}
	// Everything else in that row is the operator's, byte for byte.
	if manager.Collector.Enabled == nil || !*manager.Collector.Enabled {
		t.Fatalf("the rebind rewrote the operator's configuration: %+v", manager)
	}
	if !reflect.DeepEqual(operator.CodexInspection, manager.CodexInspection) {
		t.Fatalf("the rebind changed the operator's codex inspection config: %+v", manager.CodexInspection)
	}
	if setup := fixture.setupRow(t); setup.ManagementKey != replacement || setup.CPAUpstreamURL != fixture.baseURL {
		t.Fatalf("the setup mirror kept the replaced credential: %+v", setup)
	}
	// Nothing persisted is the plaintext candidate.
	var plaintext int
	if err = fixture.db.QueryRow(`select count(*) from settings where value like '%management-after-rotation%'`).Scan(&plaintext); err != nil {
		t.Fatal(err)
	}
	if plaintext != 0 {
		t.Fatalf("%d persisted rows carry the credential in plaintext", plaintext)
	}
	// A reopen reads the rebound installation, not a rejected record.
	_ = fixture.db.Close()
	reopened, err := sqlite.Open(fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	loaded, err := New(reopened).Load(t.Context())
	if err != nil {
		t.Fatalf("the rebound record does not reopen: %v", err)
	}
	if loaded.Connection == nil || loaded.Connection.ManagementKey != replacement ||
		loaded.Operation.Revision != rebound.Operation.Revision {
		t.Fatalf("reopened record = %+v", loaded.Operation.Revision)
	}
	if decrypted, err := fixture.protector.UnprotectString(loaded.Connection.ManagementKey); err != nil || decrypted != "management-after-rotation" {
		t.Fatalf("the reopened envelope does not decrypt: %v", err)
	}
}

// TestRebindConnectionRefusesWhatItCannotHonour proves the durability rules are
// enforced by the write itself rather than by its caller: another address, a
// credential that is not a protected envelope, a record another writer moved, and
// an installation that is not a committed Connect External target are each
// refused without changing anything.
func TestRebindConnectionRefusesWhatItCannotHonour(t *testing.T) {
	fixture := newRebindFixture(t)
	replacement, err := fixture.protector.ProtectString("management-after-rotation")
	if err != nil {
		t.Fatal(err)
	}
	before := *fixture.record.Connection
	for _, scenario := range []struct {
		name     string
		identity bootstrap.FreshIdentity
		revision uint64
		next     model.ManagerCPAConnectionConfig
		want     error
	}{
		{
			name: "another address", identity: fixture.id, revision: fixture.record.Operation.Revision,
			next: model.ManagerCPAConnectionConfig{CPABaseURL: "http://other:8317", ManagementKey: replacement},
			want: ErrRebindRefused,
		},
		{
			name: "no address", identity: fixture.id, revision: fixture.record.Operation.Revision,
			next: model.ManagerCPAConnectionConfig{ManagementKey: replacement},
			want: ErrRebindRefused,
		},
		{
			name: "plaintext credential", identity: fixture.id, revision: fixture.record.Operation.Revision,
			next: model.ManagerCPAConnectionConfig{CPABaseURL: fixture.baseURL, ManagementKey: "management-after-rotation"},
			want: ErrRebindRefused,
		},
		{
			name: "stale revision", identity: fixture.id, revision: fixture.record.Operation.Revision + 1,
			next: model.ManagerCPAConnectionConfig{CPABaseURL: fixture.baseURL, ManagementKey: replacement},
			want: ErrConflict,
		},
		{
			name: "another installation", identity: bootstrap.FreshIdentity{AdmissionID: "a", InstallationID: "i", Generation: "g", OperationID: "fresh-operation"},
			revision: fixture.record.Operation.Revision,
			next:     model.ManagerCPAConnectionConfig{CPABaseURL: fixture.baseURL, ManagementKey: replacement},
			want:     ErrConflict,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			record, err := fixture.rebinder.RebindConnection(t.Context(), scenario.identity, scenario.revision, scenario.next)
			if !errors.Is(err, scenario.want) {
				t.Fatalf("rebind = %v, want %v", err, scenario.want)
			}
			if record.Operation.Revision != 0 {
				t.Fatal("a refused rebind answered a record")
			}
			loaded, err := fixture.store.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Connection == nil || *loaded.Connection != before {
				t.Fatal("a refused rebind changed the stored connection")
			}
			if loaded.Operation.Revision != fixture.record.Operation.Revision {
				t.Fatalf("a refused rebind moved the revision to %d", loaded.Operation.Revision)
			}
		})
	}
}

// TestRebindConnectionNeedsACommittedExternalInstallation proves the checkpoint is
// the authority for this write too: a record that is not a committed Connect
// External installation has no credential to rotate, and the saturated revision
// cannot be advanced at all.
func TestRebindConnectionNeedsACommittedExternalInstallation(t *testing.T) {
	fixture := newRebindFixture(t)
	replacement, err := fixture.protector.ProtectString("management-after-rotation")
	if err != nil {
		t.Fatal(err)
	}
	// The checkpoint is the authority the capability trusts; a pending record is
	// not an installation this Manager may rotate a credential in.
	if _, err = fixture.db.Exec("update settings set value=? where key=?",
		`{"strategy":"connect_external","targetIdentity":"external:sha256:0000000000000000000000000000000000000000000000000000000000000000","targetGeneration":1}`,
		OperationKey); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.rebinder.RebindConnection(t.Context(), fixture.id, fixture.record.Operation.Revision,
		model.ManagerCPAConnectionConfig{CPABaseURL: fixture.baseURL, ManagementKey: replacement}); !errors.Is(err, ErrNotFresh) && !errors.Is(err, ErrRebindRefused) {
		t.Fatalf("a staged checkpoint was rebound: %v", err)
	}
	record := newRebindFixture(t)
	if _, err = record.rebinder.RebindConnection(t.Context(), record.id, math.MaxUint64,
		model.ManagerCPAConnectionConfig{CPABaseURL: record.baseURL, ManagementKey: replacement}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a saturated revision was rebound: %v", err)
	}
}
