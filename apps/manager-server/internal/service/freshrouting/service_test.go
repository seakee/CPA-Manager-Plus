package freshrouting

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func routeStore(t *testing.T) (freshstore.Store, bootstrap.FreshIdentity) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "manager.sqlite")
	db, e := sqlite.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	st := freshstore.New(db)
	id := bootstrap.FreshIdentity{AdmissionID: "admission", InstallationID: "install", Generation: "gen", OperationID: "fresh-operation", DatabasePath: p}
	credential, e := security.NewAdminCredential("cpamp_RoutingAdmin0123456789!", "fresh")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.Create(t.Context(), id, &credential); e != nil {
		t.Fatal(e)
	}
	return st, id
}

type routeDriver struct {
	actual                          State
	inspectErr, applyErr, verifyErr error
	applies, verifies               int
	inspects                        int
}

func (d *routeDriver) Inspect(context.Context) (State, error) {
	d.inspects++
	return d.actual, d.inspectErr
}
func (d *routeDriver) Apply(_ context.Context, s State) error {
	d.applies++
	if d.applyErr == nil {
		d.actual = s
	}
	return d.applyErr
}
func (d *routeDriver) Verify(context.Context, State) error { d.verifies++; return d.verifyErr }

func TestPrepareCollisionInspectionPrecedesAnyDurableIntent(t *testing.T) {
	for _, scenario := range []string{"foreign-owner", "foreign-generation", "foreign-installation", "foreign-target", "foreign-revision", "already-committed", "partial-unowned-state", "inspect-error", "driver-missing"} {
		t.Run(scenario, func(t *testing.T) {
			st, id := routeStore(t)
			before, err := st.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			driver := &routeDriver{actual: State{InstallationID: id.InstallationID, Generation: id.Generation, OperationID: id.OperationID, Revision: 1, Target: "/private"}}
			wantErr := error(ErrConflict)
			switch scenario {
			case "foreign-owner":
				driver.actual.OperationID = "foreign-operation"
			case "foreign-generation":
				driver.actual.Generation = "foreign"
			case "foreign-installation":
				driver.actual.InstallationID = "foreign"
			case "foreign-target":
				driver.actual.Target = "/foreign"
			case "foreign-revision":
				driver.actual.Revision++
			case "already-committed":
				driver.actual.Committed = true
			case "partial-unowned-state":
				driver.actual.OperationID = ""
			case "inspect-error":
				driver.inspectErr = ErrBlocked
				wantErr = ErrBlocked
			case "driver-missing":
				wantErr = ErrBlocked
			}
			var consumer Driver = driver
			if scenario == "driver-missing" {
				consumer = nil
			}
			if _, err := New(st, consumer).Prepare(t.Context(), id, "/private"); !errors.Is(err, wantErr) {
				t.Fatalf("Prepare: %v", err)
			}
			after, err := st.Load(t.Context())
			if err != nil || after.Operation.Revision != before.Operation.Revision || after.Operation.Routing != before.Operation.Routing {
				t.Fatal("collision/unavailable inspection wrote Fresh intent", after.Operation, err)
			}
			if driver.applies != 0 || driver.verifies != 0 {
				t.Fatal("Prepare mutated public routing")
			}
		})
	}
}

func TestPrepareValidatesAndChecksAuthorityBeforeInspectingIngress(t *testing.T) {
	st, id := routeStore(t)
	driver := &routeDriver{}
	svc := New(st, driver)
	if _, err := svc.Prepare(t.Context(), id, "/v1"); err == nil {
		t.Fatal("invalid prefix accepted")
	}
	if _, err := svc.PrepareAuthorized(t.Context(), id, "/private", func(freshstore.Record) error { return freshstore.ErrConflict }); err == nil {
		t.Fatal("invalid session accepted")
	}
	if driver.inspects != 0 {
		t.Fatal("invalid request reached collision probe")
	}
	if _, err := svc.Prepare(t.Context(), id, "/private"); err != nil || driver.inspects != 1 {
		t.Fatalf("valid Prepare: %v", err)
	}
	// An already persisted identical intent is idempotent even while the
	// consumer is temporarily unavailable; Activate still rechecks it.
	driver.inspectErr = ErrBlocked
	if _, err := svc.Prepare(t.Context(), id, "/private"); err != nil || driver.inspects != 1 {
		t.Fatalf("retry rewrote or reprobed intent: %v", err)
	}
}

func TestInitialRoutingSameOperationRestartLostResultAndCutover(t *testing.T) {
	st, id := routeStore(t)
	driver := &routeDriver{verifyErr: ErrUnverified}
	svc := New(st, driver)
	if _, e := svc.Prepare(t.Context(), id, "/v1"); e == nil {
		t.Fatal("reserved route accepted")
	}
	op, e := svc.Prepare(t.Context(), id, "/private")
	if e != nil || op.Routing.OperationID != id.OperationID || op.Routing.Stage != bootstrap.PrefixIntentPersisted {
		t.Fatal(op, e)
	}
	preparedRevision := op.Revision
	op, e = svc.Prepare(t.Context(), id, "/private")
	if e != nil || op.Revision != preparedRevision {
		t.Fatal("retries changed original intent")
	}
	if _, e = svc.Prepare(t.Context(), id, "/another"); e == nil {
		t.Fatal("new prefix owner accepted")
	}
	op, e = svc.Activate(t.Context(), id)
	if !errors.Is(e, ErrUnverified) || op.Routing.Committed || op.Routing.Stage != bootstrap.PrefixRouteActivated {
		t.Fatal(op, e)
	}
	if svc.Snapshot(t.Context()).PublicAPIActive() {
		t.Fatal("failed verify promoted desired path")
	}
	// New service simulates restart after applied route/lost verification; it
	// must inspect and reuse the exact checkpoint and actual route.
	driver.verifyErr = nil
	svc = New(st, driver)
	op, e = svc.Activate(t.Context(), id)
	if e != nil || !op.Routing.Committed || op.Routing.Stage != bootstrap.PrefixCommitted || op.Identity != id || op.Routing.Revision != 1 || !svc.Snapshot(t.Context()).PublicAPIActive() {
		t.Fatal(op, e)
	}
	applies := driver.applies
	revision := op.Revision
	op, e = svc.Activate(t.Context(), id)
	if e != nil || driver.applies != applies || op.Revision != revision {
		t.Fatal("lost commit response made another operation", e)
	}
	// Ingress restart loses only its memory projection. Restore uses the
	// committed DB checkpoint without reopening fixed auth or rewriting DB.
	driver.actual = State{}
	if e = New(st, driver).Restore(t.Context()); e != nil || !driver.actual.Committed {
		t.Fatal(e)
	}
	r, e := st.Load(t.Context())
	if e != nil || r.Operation.Revision != revision {
		t.Fatal("restore rewrote authority")
	}
}
func TestInitialRoutingRejectsForeignOwnerAndUnverifiedConsumer(t *testing.T) {
	st, id := routeStore(t)
	driver := &routeDriver{}
	svc := New(st, driver)
	_, _ = svc.Prepare(t.Context(), id, "/private")
	for _, mutation := range []func(*State){func(s *State) { s.OperationID = "foreign-prefix-operation" }, func(s *State) { s.Generation = "foreign" }, func(s *State) { s.Target = "/foreign" }, func(s *State) { s.Revision++ }} {
		r, _ := st.Load(t.Context())
		driver.actual = StateFor(r.Operation)
		mutation(&driver.actual)
		if _, e := svc.Activate(t.Context(), id); !errors.Is(e, ErrConflict) {
			t.Fatal("foreign owner accepted", e)
		}
		if e := svc.Restore(t.Context()); !errors.Is(e, ErrConflict) {
			t.Fatal("foreign restore accepted", e)
		}
	}
	if driver.verifies != 0 || driver.applies != 0 {
		t.Fatal("touched foreign route")
	}
	if _, e := New(st, nil).Activate(t.Context(), id); !errors.Is(e, ErrBlocked) {
		t.Fatal("missing consumer succeeded")
	}
}

func TestBrowserRoutingAuthorityRecheckedAtPrepareApplyAndCommit(t *testing.T) {
	st, id := routeStore(t)
	driver := &routeDriver{}
	svc := New(st, driver)
	rejected := func(freshstore.Record) error { return freshstore.ErrConflict }
	if _, e := svc.PrepareAuthorized(t.Context(), id, "/private", rejected); e == nil {
		t.Fatal("stale session wrote intent")
	}
	r, _ := st.Load(t.Context())
	if r.Operation.Routing.Target != "" {
		t.Fatal("unauthorized intent persisted")
	}
	if _, e := svc.Prepare(t.Context(), id, "/private"); e != nil {
		t.Fatal(e)
	}
	if _, e := svc.ActivateAuthorized(t.Context(), id, rejected); e == nil || driver.applies != 0 {
		t.Fatal("stale session applied route")
	}
	// The fourth check is inside the final commit transaction, after network
	// verification and the durable health checkpoint. An expired/reissued
	// session cannot cause authentication cutover from its earlier authority.
	checks := 0
	guard := func(freshstore.Record) error {
		checks++
		if checks >= 4 {
			return freshstore.ErrConflict
		}
		return nil
	}
	if _, e := svc.ActivateAuthorized(t.Context(), id, guard); e == nil {
		t.Fatal("expired final authority committed route")
	}
	r, _ = st.Load(t.Context())
	if r.Operation.Routing.Committed || r.Operation.Routing.Stage != bootstrap.PrefixHealthVerified || svc.Snapshot(t.Context()).PublicAPIActive() {
		t.Fatal("final guard bypass", r.Operation)
	}
}
