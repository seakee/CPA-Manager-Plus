package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestPluginResourceUsesManagedCompletionMirror(t *testing.T) {
	const managementKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/resource/plugins/example" {
			t.Errorf("upstream path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+managementKey {
			t.Errorf("upstream authorization = %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("managed-resource"))
	}))
	defer upstream.Close()
	baseURL := strings.TrimRight(upstream.URL, "/")

	path := filepath.Join(t.TempDir(), "manager.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fresh := freshstore.New(db)
	id := bootstrap.FreshIdentity{AdmissionID: "admission", InstallationID: "installation", Generation: "generation", OperationID: "operation", DatabasePath: path}
	admin, err := security.NewAdminCredential("Cpamp_Resource_Mirror_Admin_839216", "host")
	if err != nil {
		t.Fatal(err)
	}
	record, err := fresh.Create(context.Background(), id, &admin)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector([]byte("resource mirror data key"))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := protector.ProtectString(managementKey)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, Stage: "committed", TargetIdentity: "runtime", TargetGeneration: 7, TargetArtifactID: "sha256:" + strings.Repeat("a", 64), DestinationConnectionIdentity: freshstrategy.ManagedDestinationIdentity(baseURL)}
	pending, err := fresh.Update(context.Background(), id, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = checkpoint
		r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: baseURL, ManagementKey: ciphertext}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fresh.Update(context.Background(), id, pending.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing.OperationID = id.OperationID
		r.Operation.Routing.Revision = 1
		r.Operation.Routing.Target = "/console"
		r.Operation.Routing.InferenceTarget = "/access-api"
		r.Operation.Routing.Stage = bootstrap.PrefixCommitted
		r.Operation.Routing.Committed = true
		r.Operation.Routing.InferenceCommitted = true
		r.Operation.ProductComplete = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	st := store.New(db, protector)
	svc := New(managerconfig.New(config.Config{}, st, nil), st)
	req := httptest.NewRequest(http.MethodGet, "/v0/resource/plugins/example", nil)
	// The Console frames a plugin page same-origin; that document load is the
	// only same-origin destination the plugin resource policy admits.
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Dest", "iframe")
	recorder := httptest.NewRecorder()
	svc.ProxyPluginResource(recorder, req, func(w http.ResponseWriter, status int, err error) {
		http.Error(w, err.Error(), status)
	})
	if recorder.Code != http.StatusOK || strings.TrimSpace(recorder.Body.String()) != "managed-resource" {
		t.Fatal("resource bridge did not use the completed managed setup", recorder.Code, recorder.Body.String())
	}
}
