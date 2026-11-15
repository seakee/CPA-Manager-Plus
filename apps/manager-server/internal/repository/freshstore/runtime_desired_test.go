package freshstore

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func runtimeDesiredTestIdentity(path string) bootstrap.FreshIdentity {
	return bootstrap.FreshIdentity{AdmissionID: "admission", InstallationID: "installation", Generation: "generation", OperationID: "operation", DatabasePath: path}
}

func runtimeDesiredTestProtector(t *testing.T) *security.Protector {
	t.Helper()
	protector, err := security.NewProtector([]byte("fresh runtime desired test data key"))
	if err != nil {
		t.Fatal(err)
	}
	return protector
}

func runtimeDesiredTestCompleted(t *testing.T, db *sql.DB, path string) (bootstrap.FreshIdentity, Record) {
	t.Helper()
	id := runtimeDesiredTestIdentity(path)
	st := New(db)
	admin, err := security.NewAdminCredential("Cpamp_Config_Admin_Secret_839216", "host")
	if err != nil {
		t.Fatal(err)
	}
	record, err := st.Create(t.Context(), id, &admin)
	if err != nil {
		t.Fatal(err)
	}
	record, err = st.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: id.OperationID, Revision: 1, Target: "/console", Stage: bootstrap.PrefixCommitted, Committed: true, SuggestedControlBasePath: r.Operation.Routing.SuggestedControlBasePath, SuggestedInferenceBasePath: r.Operation.Routing.SuggestedInferenceBasePath}
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, Stage: "committed", TargetIdentity: "runtime", TargetGeneration: 1, TargetArtifactID: "sha256:" + strings.Repeat("a", 64)}
		r.Operation.ProductComplete = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return id, record
}

// A Runtime stop is delivered product behaviour: the Fresh Runtime page persists
// the desired lifecycle through the shared setting repository. The record must
// keep loading across that write and across a Manager restart, otherwise the
// first stop makes the Manager unloadable (/health 503, /console 404).
func TestCompletedFreshRecordSurvivesRuntimeStop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, record := runtimeDesiredTestCompleted(t, db, path)
	settings := setting.New(db, runtimeDesiredTestProtector(t))
	stopped, err := settings.SetEmbeddedRuntimeDesiredLifecycle(t.Context(), model.EmbeddedRuntimeDesiredStopped)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.DesiredLifecycle != model.EmbeddedRuntimeDesiredStopped {
		t.Fatalf("desired lifecycle = %+v", stopped)
	}
	loaded, err := New(db).Load(t.Context())
	if err != nil {
		t.Fatal("Runtime stop made the completed Fresh record unloadable:", err)
	}
	if loaded.Operation.Identity != id || !loaded.Operation.ProductComplete || loaded.Operation.Revision != record.Operation.Revision {
		t.Fatalf("Runtime stop changed Fresh ownership: %+v", loaded.Operation)
	}
	// The Fresh CAS keeps writing beside the desired lifecycle: a stop must not
	// freeze the installation journal.
	updated, err := New(db).Update(t.Context(), id, loaded.Operation.Revision, func(*Record) error { return nil })
	if err != nil {
		t.Fatal("Fresh CAS refused a write after a Runtime stop:", err)
	}
	if updated.Operation.Revision != loaded.Operation.Revision+1 {
		t.Fatalf("revision = %d", updated.Operation.Revision)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	// The stop survives the restart that follows it, and the record stays
	// admitted with it.
	reopened, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err = New(reopened).Load(t.Context()); err != nil {
		t.Fatal("completed checkpoint did not reopen after a Runtime stop:", err)
	}
	restored, found, err := setting.New(reopened, runtimeDesiredTestProtector(t)).LoadEmbeddedRuntimeDesiredState(t.Context())
	if err != nil || !found || restored.DesiredLifecycle != model.EmbeddedRuntimeDesiredStopped || restored.Revision != stopped.Revision {
		t.Fatalf("desired lifecycle after restart = %+v found=%v err=%v", restored, found, err)
	}
	// The existing Config service persists its compatibility bootstrap state on
	// the same delivered surface. It is a current row of a completed install,
	// not a legacy marker.
	settings = setting.New(reopened, runtimeDesiredTestProtector(t))
	if err = settings.SaveBootstrapState(t.Context(), model.BootstrapState{Version: 1, Status: "ready", AdminReady: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = New(reopened).Load(t.Context()); err != nil {
		t.Fatal("compatibility bootstrap state made the completed Fresh record unloadable:", err)
	}
}

// The desired lifecycle belongs to the Runtime domain, exactly like a damaged
// config field belongs to its own field: unreadable bytes there must not take
// the whole Manager down with them.
func TestDamagedDesiredLifecycleLocksOnlyTheRuntimeDomain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runtimeDesiredTestCompleted(t, db, path)
	settings := setting.New(db, runtimeDesiredTestProtector(t))
	if _, err = settings.SetEmbeddedRuntimeDesiredLifecycle(t.Context(), model.EmbeddedRuntimeDesiredStopped); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("update settings set value='{' where key='runtime_desired_v1'"); err != nil {
		t.Fatal(err)
	}
	if _, err = New(db).Load(t.Context()); err != nil {
		t.Fatal("damaged desired lifecycle locked the Manager record:", err)
	}
	if _, found, err := settings.LoadEmbeddedRuntimeDesiredState(t.Context()); err == nil || found {
		t.Fatal("damaged desired lifecycle was reported as usable")
	}
}

// A pending installation owns no Runtime lifecycle yet, so the record stays a
// legacy marker there even when its payload is a valid current value.
func TestPendingFreshRejectsValidDesiredLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id := runtimeDesiredTestIdentity(path)
	if _, err = New(db).Create(t.Context(), id, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = setting.New(db, runtimeDesiredTestProtector(t)).SetEmbeddedRuntimeDesiredLifecycle(t.Context(), model.EmbeddedRuntimeDesiredStopped); err != nil {
		t.Fatal(err)
	}
	if _, err = New(db).Load(t.Context()); !errors.Is(err, ErrNotFresh) {
		t.Fatalf("pending desired lifecycle admitted: %v", err)
	}
}
