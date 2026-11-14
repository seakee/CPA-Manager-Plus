package freshstore

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func TestManagedCompletionCASMirrorsManagerConfigAndSetup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := New(db)
	id := bootstrap.FreshIdentity{AdmissionID: "admission", InstallationID: "installation", Generation: "generation", OperationID: "operation", DatabasePath: path}
	admin, err := security.NewAdminCredential("Cpamp_Managed_Mirror_Admin_839216", "host")
	if err != nil {
		t.Fatal(err)
	}
	record, err := st.Create(t.Context(), id, &admin)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector([]byte("managed completion mirror data key"))
	if err != nil {
		t.Fatal(err)
	}
	const baseURL = "http://cpamp-runtime:8317"
	const managementKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	ciphertext, err := protector.ProtectString(managementKey)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(baseURL))
	checkpoint := bootstrap.FreshStrategyCheckpoint{
		Strategy: bootstrap.NewManaged, Stage: "committed", TargetIdentity: "runtime", TargetGeneration: 7,
		TargetArtifactID:              "sha256:" + strings.Repeat("a", 64),
		DestinationConnectionIdentity: "managed:sha256:" + hex.EncodeToString(digest[:]),
	}
	connection := &model.ManagerCPAConnectionConfig{CPABaseURL: baseURL, ManagementKey: ciphertext}
	pending, err := st.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error {
		r.Operation.Strategy = checkpoint
		r.Connection = connection
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if pending.Operation.ProductComplete {
		t.Fatal("strategy commit completed the product")
	}
	var setupRows int
	if err := db.QueryRow("select count(*) from settings where key='setup'").Scan(&setupRows); err != nil || setupRows != 0 {
		t.Fatalf("pending managed install wrote setup mirror: rows=%d err=%v", setupRows, err)
	}
	completed, err := st.Update(t.Context(), id, pending.Operation.Revision, func(r *Record) error {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: id.OperationID, Revision: 1, Target: "/console", Stage: bootstrap.PrefixCommitted, Committed: true, SuggestedControlBasePath: r.Operation.Routing.SuggestedControlBasePath, SuggestedInferenceBasePath: r.Operation.Routing.SuggestedInferenceBasePath}
		r.Operation.ProductComplete = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !completed.Operation.ProductComplete {
		t.Fatal("completion CAS did not mark ProductComplete")
	}
	settings := setting.New(db, protector)
	managerConfig, ok, err := settings.LoadManagerConfig(t.Context())
	if err != nil || !ok {
		t.Fatalf("manager_config_v1 missing after completion CAS: ok=%v err=%v", ok, err)
	}
	setup, ok, err := settings.LoadSetup(t.Context())
	if err != nil || !ok {
		t.Fatalf("setup mirror missing after completion CAS: ok=%v err=%v", ok, err)
	}
	for name, got := range map[string]struct {
		base string
		key  string
	}{
		"manager config": {managerConfig.CPAConnection.CPABaseURL, managerConfig.CPAConnection.ManagementKey},
		"setup":          {setup.CPAUpstreamURL, setup.ManagementKey},
	} {
		if got.base != baseURL || got.key != managementKey {
			t.Fatalf("%s resolved %#v, want base=%q key=%q", name, got, baseURL, managementKey)
		}
	}
	loaded, err := st.Load(t.Context())
	if err != nil || loaded.Connection == nil || loaded.Connection.CPABaseURL != baseURL {
		t.Fatalf("completed fresh record lost connection: %#v %v", loaded.Connection, err)
	}
}
