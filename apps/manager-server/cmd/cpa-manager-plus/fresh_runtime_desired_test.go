package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

// failingFreshStore answers every read with one store-level failure, which is
// what a damaged database or an unreadable checkpoint surfaces as.
type failingFreshStore struct {
	freshstore.Store
	err error
}

func (s failingFreshStore) Load(context.Context) (freshstore.Record, error) {
	return freshstore.Record{}, s.err
}

// committedManagedCheckpoint is a completed Manager-owned installation: the
// destination identity is the one fact the reconcile gate trusts, so a fixture
// without it is an unmigrated checkpoint and drives nothing.
func committedManagedCheckpoint() bootstrap.FreshStrategyCheckpoint {
	return bootstrap.FreshStrategyCheckpoint{
		Strategy: bootstrap.NewManaged, Stage: "committed",
		TargetIdentity: "runtime-test", TargetGeneration: 7,
		TargetArtifactID:              "sha256:" + strings.Repeat("a", 64),
		DestinationConnectionIdentity: "managed:sha256:" + strings.Repeat("b", 64),
	}
}

// unmigratedManagedCheckpoint is a committed New Managed checkpoint written
// before the Manager recorded who staged the destination. It carries no
// authority over the Runtime.
func unmigratedManagedCheckpoint() bootstrap.FreshStrategyCheckpoint {
	checkpoint := committedManagedCheckpoint()
	checkpoint.DestinationConnectionIdentity = ""
	return checkpoint
}

// externalCheckpoint is a Connect External installation, whose Runtime belongs
// to the operator's CPA at the configured address.
func externalCheckpoint() bootstrap.FreshStrategyCheckpoint {
	return bootstrap.FreshStrategyCheckpoint{
		Strategy: bootstrap.ConnectExternal, Stage: "committed",
		TargetIdentity: "external:sha256:" + strings.Repeat("c", 64), TargetGeneration: 1,
	}
}

// completedRuntimeTestApplication promotes the shared Fresh fixture to a
// completed installation so the canonical Runtime resource is served through
// the real ProductComplete gate.
func completedRuntimeTestApplication(t *testing.T) (*freshApplication, bootstrap.FreshOperation) {
	t.Helper()
	app, closeApp := newA3TestApp(t)
	t.Cleanup(closeApp)
	op, err := app.current(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	record, err := app.store.Update(t.Context(), op.Identity, op.Revision, func(record *freshstore.Record) error {
		record.Operation.Strategy = committedManagedCheckpoint()
		record.Operation.ProductComplete = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return app, record.Operation
}

// A successful stop is delivered product behaviour: it persists the Manager's
// desired lifecycle beside the Fresh checkpoint. The next request reloads that
// checkpoint, so the stop must leave the Manager healthy and still able to
// report the operation it recorded — otherwise the first stop takes the whole
// Console and every lane resource down until the container is recreated.
func TestRuntimeStopKeepsTheManagerLoadableAndQueryable(t *testing.T) {
	app, op := completedRuntimeTestApplication(t)
	client := &freshRuntimeTestClient{status: freshRuntimeReadyStatus(model.RuntimeCapabilityStart, model.RuntimeCapabilityStop)}
	app.runtime = client
	app.runtimeExternal = nil

	stop := httptest.NewRecorder()
	app.runtimeOperations(stop, freshRuntimeRequest(t, "stop", "stop-after-complete"), op, "runtime/operations")
	if stop.Code != http.StatusOK || client.stops != 1 {
		t.Fatalf("stop = %d stops=%d body=%s", stop.Code, client.stops, stop.Body.String())
	}
	desired, found, err := app.runtimeSettings.LoadEmbeddedRuntimeDesiredState(t.Context())
	if err != nil || !found || desired.DesiredLifecycle != model.EmbeddedRuntimeDesiredStopped {
		t.Fatalf("desired lifecycle after stop = %+v found=%v err=%v", desired, found, err)
	}

	// The liveness probe re-reads the validated store on every call.
	health := httptest.NewRecorder()
	app.ServeHTTP(health, httptest.NewRequest(http.MethodGet, bootstrap.FixedLivenessPath, nil))
	if health.Code != http.StatusOK {
		t.Fatalf("Manager health after stop = %d %s", health.Code, health.Body.String())
	}

	// The canonical resource reloads the same store before answering.
	query := httptest.NewRequest(http.MethodGet, "/control/api/v2/runtime/operations/runtime-ui/v1:stop-after-complete", nil)
	query.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	response := httptest.NewRecorder()
	app.canonicalAPI(response, query)
	if response.Code != http.StatusOK {
		t.Fatalf("operation query after stop = %d body=%s", response.Code, response.Body.String())
	}
	var operation struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &operation); err != nil {
		t.Fatalf("operation query body: %v", err)
	}
	if operation.State != string(model.RuntimeOperationSucceeded) {
		t.Fatalf("operation state after stop = %q body=%s", operation.State, response.Body.String())
	}
}

// A completed record whose desired lifecycle bytes are unreadable locks the
// Runtime domain, not the Manager. The trust model trusts the Docker host and the
// Compose operator, so this test does not treat the bytes as an attacker forging
// a record: it proves the outage this fence buys — every Runtime mutation is
// refused with a typed code before any Supervisor call, while the Manager record
// and the rest of the product keep working.
func TestUnreadableDesiredLifecycleLocksOnlyTheRuntimeDomain(t *testing.T) {
	app, _ := completedRuntimeTestApplication(t)
	client := &freshRuntimeTestClient{status: freshRuntimeReadyStatus(model.RuntimeCapabilityStart, model.RuntimeCapabilityStop)}
	app.runtime = client
	app.runtimeExternal = nil
	if _, err := app.runtimeSettings.SetEmbeddedRuntimeDesiredLifecycle(t.Context(), model.EmbeddedRuntimeDesiredRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := app.runtimeJournal.db.Exec("update settings set value='{' where key='runtime_desired_v1'"); err != nil {
		t.Fatal(err)
	}
	// ProductComplete still admits the record: the damaged row belongs to one
	// domain, not to the installation.
	op, err := app.current(t.Context())
	if err != nil || !op.ProductComplete {
		t.Fatalf("damaged desired lifecycle hid the Fresh record: %+v %v", op, err)
	}

	refused := httptest.NewRecorder()
	app.runtimeOperations(refused, freshRuntimeRequest(t, "stop", "stop-damaged-desired"), op, "runtime/operations")
	if refused.Code != http.StatusServiceUnavailable || !strings.Contains(refused.Body.String(), "desired_lifecycle_unavailable") {
		t.Fatalf("damaged desired lifecycle mutation = %d body=%s", refused.Code, refused.Body.String())
	}
	// The refusal happens before the Supervisor is asked and before the journal
	// records anything, so no write can be left unknown by the damaged row.
	if client.stops != 0 || client.starts != 0 {
		t.Fatalf("damaged desired lifecycle reached the Supervisor: starts=%d stops=%d", client.starts, client.stops)
	}
	if journal, loadErr := app.runtimeJournal.load(t.Context()); loadErr != nil || len(journal.Operations) != 0 {
		t.Fatalf("damaged desired lifecycle wrote the journal: %+v %v", journal, loadErr)
	}
	// The rest of the Manager stays usable: liveness answers, the key list still
	// reads through the same record, and the damaged bytes stay unreadable so the
	// reconciler never converges on them.
	health := httptest.NewRecorder()
	app.ServeHTTP(health, httptest.NewRequest(http.MethodGet, bootstrap.FixedLivenessPath, nil))
	if health.Code != http.StatusOK {
		t.Fatalf("Manager health with a damaged desired lifecycle = %d", health.Code)
	}
	keys := httptest.NewRequest(http.MethodGet, "/control/api/v2/api-keys", nil)
	keys.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	keysResponse := httptest.NewRecorder()
	app.canonicalAPI(keysResponse, keys)
	if keysResponse.Code != http.StatusOK {
		t.Fatalf("key list with a damaged desired lifecycle = %d %s", keysResponse.Code, keysResponse.Body.String())
	}
	if _, found, err := app.runtimeSettings.LoadEmbeddedRuntimeDesiredState(t.Context()); err == nil || found {
		t.Fatal("damaged desired lifecycle was reported as usable")
	}
}

// Only an incomplete installation pins the reconcile Start to the artifact its
// wizard committed. A completed installation has no pin: `activate_update`
// replaces the active engine in place and keeps the install-time artifact in the
// checkpoint as audit evidence, so a pin here would refuse every restart after an
// update.
func TestCommittedArtifactPinsOnlyAnIncompleteInstallation(t *testing.T) {
	harness := newManagedInstallHarness(t)
	app := harness.app
	committed := managedInstallRuntimeStatus().ActiveGatewayArtifact.ArtifactID

	authority, ok := app.desiredStateStore().(runtimeservice.CommittedArtifactStore)
	if !ok {
		t.Fatal("the Manager-owned desired state store does not publish the committed artifact")
	}

	// The harness installation is complete: no pin, whatever the observation says.
	for _, status := range []model.RuntimeObservedStatus{
		freshRuntimeReadyStatus(),
		freshRuntimeReadyStatus(model.RuntimeCapabilityStart),
		managedInstallRuntimeStatus(),
	} {
		app.runtime = &freshRuntimeTestClient{status: status}
		if artifactID, found, err := authority.LoadEmbeddedRuntimeCommittedArtifact(t.Context()); err != nil || found || artifactID != "" {
			t.Fatalf("completed installation artifact = (%q, %v, %v), want none", artifactID, found, err)
		}
	}

	// Until the installation completes it is pinned to its committed target.
	incomplete := newManagedInstallHarnessWith(t, func(r *freshstore.Record, env managedInstallEnv) {
		completedManagedInstall(r, env)
		r.Operation.ProductComplete = false
	}, true)
	incompleteAuthority, ok := incomplete.app.desiredStateStore().(runtimeservice.CommittedArtifactStore)
	if !ok {
		t.Fatal("the incomplete desired state store does not publish the committed artifact")
	}
	if artifactID, found, err := incompleteAuthority.LoadEmbeddedRuntimeCommittedArtifact(t.Context()); err != nil || !found || artifactID != committed {
		t.Fatalf("incomplete installation artifact = (%q, %v, %v), want %q", artifactID, found, err, committed)
	}

	// An unmigrated or operator-managed checkpoint was never staged by this
	// Manager, so it carries no committed engine of its own.
	unmanaged, _ := completedRuntimeTestApplication(t)
	overrideCheckpoint(unmanaged, unmigratedManagedCheckpoint())
	unmanagedAuthority, ok := unmanaged.desiredStateStore().(runtimeservice.CommittedArtifactStore)
	if !ok {
		t.Fatal("the completed desired state store does not publish the committed artifact")
	}
	if artifactID, found, err := unmanagedAuthority.LoadEmbeddedRuntimeCommittedArtifact(t.Context()); err != nil || found || artifactID != "" {
		t.Fatalf("unmigrated managed checkpoint artifact = (%q, %v, %v), want none", artifactID, found, err)
	}
}

// checkpointOverrideStore answers Load with the real record and one substituted
// checkpoint, so a test can put any strategy in front of the production gate
// without materialising a checkpoint the store's own validation would reject.
// The real store still backs every other read, which is what makes the persisted
// settings row and the substituted checkpoint meet in the same application.
type checkpointOverrideStore struct {
	freshstore.Store
	mu         sync.Mutex
	checkpoint bootstrap.FreshStrategyCheckpoint
	err        error
}

func (s *checkpointOverrideStore) Load(ctx context.Context) (freshstore.Record, error) {
	s.mu.Lock()
	checkpoint, err := s.checkpoint, s.err
	s.mu.Unlock()
	if err != nil {
		return freshstore.Record{}, err
	}
	record, loadErr := s.Store.Load(ctx)
	if loadErr != nil {
		return freshstore.Record{}, loadErr
	}
	record.Operation.Strategy = checkpoint
	return record, nil
}

func (s *checkpointOverrideStore) commit(checkpoint bootstrap.FreshStrategyCheckpoint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpoint = checkpoint
}

// fail makes every subsequent read answer with this error, which is how a
// damaged or unreadable checkpoint reaches the gate.
func (s *checkpointOverrideStore) fail(err error) *checkpointOverrideStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
	return s
}

// overrideCheckpoint installs one substituted checkpoint in front of the
// production store and returns the handle a test uses to change it mid-run. The
// application is owned by the test, so rewiring its store is local: the real
// store still backs every other read, which is what makes the persisted settings
// row and the substituted checkpoint meet in the same application.
func overrideCheckpoint(app *freshApplication, checkpoint bootstrap.FreshStrategyCheckpoint) *checkpointOverrideStore {
	store := &checkpointOverrideStore{Store: app.store, checkpoint: checkpoint}
	app.store = store
	return store
}

// The reconcile gate runs on every cycle and asks one question: does the
// committed Fresh checkpoint name an installation this Manager staged? Every
// other answer must yield no desired state at all, so a runtime_desired_v1 row
// that outlives a mode change can never drive a Start or a Stop.
func TestDesiredStateGateServesOnlyManagerOwnedInstallations(t *testing.T) {
	for name, test := range map[string]struct {
		prepare func(*freshApplication)
		wantErr bool
		want    bool
	}{
		"committed managed installation": {want: true},
		"connect external": {
			prepare: func(app *freshApplication) { overrideCheckpoint(app, externalCheckpoint()) },
		},
		"uncommitted operation": {
			prepare: func(app *freshApplication) {
				overrideCheckpoint(app, bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, Stage: "staged"})
			},
		},
		"unmigrated managed": {
			prepare: func(app *freshApplication) { overrideCheckpoint(app, unmigratedManagedCheckpoint()) },
		},
		"nothing committed yet": {
			prepare: func(app *freshApplication) { overrideCheckpoint(app, bootstrap.FreshStrategyCheckpoint{}) },
		},
		"unreadable checkpoint": {
			prepare: func(app *freshApplication) {
				overrideCheckpoint(app, committedManagedCheckpoint()).fail(errors.New("Fresh store unavailable"))
			},
			wantErr: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			app, _ := completedRuntimeTestApplication(t)
			// The row is present and says running: exactly the state that must not
			// leak past the gate once the checkpoint stops naming a Manager-owned
			// installation.
			if _, err := app.runtimeSettings.SetEmbeddedRuntimeDesiredLifecycle(t.Context(), model.EmbeddedRuntimeDesiredRunning); err != nil {
				t.Fatal(err)
			}
			if test.prepare != nil {
				test.prepare(app)
			}
			desired, found, err := app.desiredStateStore().LoadEmbeddedRuntimeDesiredState(t.Context())
			if test.wantErr {
				// An unreadable checkpoint mutates nothing and reports the failure
				// instead of quietly degrading to "no desired state".
				if err == nil || found {
					t.Fatalf("desired state = (%+v, %v, %v), want an error", desired, found, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadEmbeddedRuntimeDesiredState() error = %v", err)
			}
			if found != test.want {
				t.Fatalf("desired state found = %v, want %v", found, test.want)
			}
			if found && desired.DesiredLifecycle != model.EmbeddedRuntimeDesiredRunning {
				t.Fatalf("desired lifecycle = %q", desired.DesiredLifecycle)
			}
		})
	}
}

// The gate is only worth anything if it reaches the Supervisor as "no mutation".
// A persisted running desire would start an offline Runtime and a persisted
// stopped desire would stop a ready one; neither may happen for a checkpoint that
// does not name a Manager-owned installation.
func TestNonManagerOwnedCheckpointNeverMutatesTheRuntime(t *testing.T) {
	offline := freshRuntimeReadyStatus(model.RuntimeCapabilityStart, model.RuntimeCapabilityStop)
	offline.State = model.RuntimeStateOffline
	ready := freshRuntimeReadyStatus(model.RuntimeCapabilityStart, model.RuntimeCapabilityStop)
	for name, test := range map[string]struct {
		checkpoint bootstrap.FreshStrategyCheckpoint
		desired    model.EmbeddedRuntimeDesiredLifecycle
		status     model.RuntimeObservedStatus
	}{
		"connect external cannot be started": {
			checkpoint: externalCheckpoint(), desired: model.EmbeddedRuntimeDesiredRunning, status: offline,
		},
		"connect external cannot be stopped": {
			checkpoint: externalCheckpoint(), desired: model.EmbeddedRuntimeDesiredStopped, status: ready,
		},
		"uncommitted operation cannot be started": {
			checkpoint: bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, Stage: "staged"},
			desired:    model.EmbeddedRuntimeDesiredRunning, status: offline,
		},
		"unmigrated managed cannot be stopped": {
			checkpoint: unmigratedManagedCheckpoint(), desired: model.EmbeddedRuntimeDesiredStopped, status: ready,
		},
	} {
		t.Run(name, func(t *testing.T) {
			app, _ := completedRuntimeTestApplication(t)
			if _, err := app.runtimeSettings.SetEmbeddedRuntimeDesiredLifecycle(t.Context(), test.desired); err != nil {
				t.Fatal(err)
			}
			overrideCheckpoint(app, test.checkpoint)
			client := &freshRuntimeTestClient{status: test.status}
			app.runtime = client
			reconciler := runtimeservice.NewReconciler(client, app.desiredStateStore(), runtimeservice.DefaultReconcileInterval, nil)
			if err := reconciler.ReconcileOnce(t.Context()); err != nil {
				t.Fatalf("ReconcileOnce() error = %v", err)
			}
			if client.starts != 0 || client.stops != 0 {
				t.Fatalf("non-owned checkpoint mutated the Runtime: starts=%d stops=%d", client.starts, client.stops)
			}
		})
	}

	// A store the Manager cannot read is a refusal, not a green light: the cycle
	// reports the failure and still submits nothing.
	t.Run("unreadable checkpoint reports and mutates nothing", func(t *testing.T) {
		app, _ := completedRuntimeTestApplication(t)
		if _, err := app.runtimeSettings.SetEmbeddedRuntimeDesiredLifecycle(t.Context(), model.EmbeddedRuntimeDesiredRunning); err != nil {
			t.Fatal(err)
		}
		overrideCheckpoint(app, committedManagedCheckpoint()).fail(errors.New("Fresh store unavailable"))
		client := &freshRuntimeTestClient{status: offline}
		app.runtime = client
		reconciler := runtimeservice.NewReconciler(client, app.desiredStateStore(), runtimeservice.DefaultReconcileInterval, nil)
		if err := reconciler.ReconcileOnce(t.Context()); err == nil {
			t.Fatal("ReconcileOnce() with an unreadable checkpoint = nil, want an error")
		}
		if client.starts != 0 || client.stops != 0 {
			t.Fatalf("unreadable checkpoint mutated the Runtime: starts=%d stops=%d", client.starts, client.stops)
		}
	})
}

// One worker spans installation commits, so the gate cannot be a startup
// decision. An operation that commits `connect_external` after the process
// started must stop being driven from that cycle on - and it must start being
// driven again only if a Manager-owned installation is restored.
func TestReconcileGateFollowsACommitThatFlipsToConnectExternal(t *testing.T) {
	app, _ := completedRuntimeTestApplication(t)
	if _, err := app.runtimeSettings.SetEmbeddedRuntimeDesiredLifecycle(t.Context(), model.EmbeddedRuntimeDesiredRunning); err != nil {
		t.Fatal(err)
	}
	offline := freshRuntimeReadyStatus(model.RuntimeCapabilityStart, model.RuntimeCapabilityStop)
	offline.State = model.RuntimeStateOffline
	client := &freshRuntimeTestClient{status: offline}

	store := overrideCheckpoint(app, bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, Stage: "staged"})
	app.runtime = client
	reconciler := runtimeservice.NewReconciler(client, app.desiredStateStore(), runtimeservice.DefaultReconcileInterval, nil)

	// The same worker instance owns all three cycles.
	if err := reconciler.ReconcileOnce(t.Context()); err != nil {
		t.Fatalf("ReconcileOnce(uncommitted) = %v", err)
	}
	if client.starts != 0 {
		t.Fatalf("uncommitted operation started the Runtime: starts=%d", client.starts)
	}
	store.commit(externalCheckpoint())
	offline.Generation = 8 // a restart between the two cycles
	client.status = offline
	if err := reconciler.ReconcileOnce(t.Context()); err != nil {
		t.Fatalf("ReconcileOnce(connect external) = %v", err)
	}
	if client.starts != 0 {
		t.Fatalf("Connect External commit started the Runtime: starts=%d", client.starts)
	}
	// Restoring a Manager-owned checkpoint proves the two negatives above were
	// the gate and not a worker that had stopped deciding.
	store.commit(committedManagedCheckpoint())
	offline.Generation = 9
	client.status = offline
	if err := reconciler.ReconcileOnce(t.Context()); err != nil {
		t.Fatalf("ReconcileOnce(committed managed) = %v", err)
	}
	if client.starts != 1 {
		t.Fatalf("Manager-owned checkpoint starts = %d, want 1", client.starts)
	}
}

// An unreadable checkpoint is a refusal, not an unfenced automatic start: the
// Manager must never launch an engine it cannot name.
func TestCommittedArtifactReadFailureRefusesToNameAnEngine(t *testing.T) {
	store := failingFreshStore{err: errors.New("Fresh store unavailable")}
	authority := freshDesiredStateStore{app: &freshApplication{store: store}}
	if artifactID, found, err := authority.LoadEmbeddedRuntimeCommittedArtifact(t.Context()); err == nil || found || artifactID != "" {
		t.Fatalf("unreadable checkpoint artifact = (%q, %v, %v), want an error", artifactID, found, err)
	}
}

// An in-place engine update swaps the Supervisor's active artifact and rotates
// the generation; the Fresh checkpoint still names the install-time engine. The
// restart must start the updated engine, so the reconcile Start fences the
// same-cycle observation.
func TestRestartAfterInPlaceUpdateStartsTheUpdatedEngine(t *testing.T) {
	app, op := completedRuntimeTestApplication(t)
	updated := model.RuntimeArtifactID("sha256:" + strings.Repeat("e", 64))
	checkpoint := op.Strategy.TargetArtifactID
	if updated == model.RuntimeArtifactID(checkpoint) {
		t.Fatalf("fixture artifact %q is not distinguishable from the update", checkpoint)
	}
	if _, err := app.runtimeSettings.SetEmbeddedRuntimeDesiredLifecycle(t.Context(), model.EmbeddedRuntimeDesiredRunning); err != nil {
		t.Fatal(err)
	}

	restarted := freshRuntimeReadyStatus(model.RuntimeCapabilityStart, model.RuntimeCapabilityStop)
	restarted.Generation = 4242 // new Supervisor generation after the container restart
	restarted.State = model.RuntimeStateOffline
	restarted.ActiveGatewayArtifact = &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: updated}
	client := &freshRuntimeTestClient{status: restarted}
	app.runtime = client
	app.runtimeExternal = nil

	reconciler := runtimeservice.NewReconciler(app.runtime, app.desiredStateStore(), runtimeservice.DefaultReconcileInterval, nil)
	if err := reconciler.ReconcileOnce(t.Context()); err != nil {
		t.Fatalf("ReconcileOnce() after an in-place update = %v", err)
	}
	if client.starts != 1 {
		t.Fatalf("Supervisor starts after the update = %d, want 1", client.starts)
	}
	if got := client.startRequests[0].ExpectedActiveArtifactID; got != updated {
		t.Fatalf("automatic Start artifact = %q, want the updated engine %q", got, updated)
	}
}

func TestCompletedManagedInstallAutoStartsOfflineRuntimeAfterRestartAndPreservesExplicitStop(t *testing.T) {
	app, op := completedRuntimeTestApplication(t)
	offlineAfterRestart := freshRuntimeReadyStatus(model.RuntimeCapabilityStart, model.RuntimeCapabilityStop)
	offlineAfterRestart.Generation = 991234 // new Supervisor generation after container restart
	offlineAfterRestart.State = model.RuntimeStateOffline
	offlineAfterRestart.Recovery = &model.RuntimeRecoveryObservation{
		State:             model.RuntimeRecoveryStateInactive,
		AttemptsRemaining: 0,
	}
	client := &freshRuntimeTestClient{status: offlineAfterRestart}
	app.runtime = client
	app.runtimeExternal = nil

	// An unmigrated or operator-managed checkpoint without DestinationConnectionIdentity
	// must never seed runtime_desired_v1.
	unmigrated := op
	unmigrated.Strategy.DestinationConnectionIdentity = ""
	if unmigratedSeeded, seedErr := app.ensureManagedRuntimeDesiredRunning(t.Context(), unmigrated); seedErr != nil || unmigratedSeeded {
		t.Fatalf("ensureManagedRuntimeDesiredRunning without destination identity = (%v, %v), want (false, nil)", unmigratedSeeded, seedErr)
	}
	seeded, err := app.ensureManagedRuntimeDesiredRunning(t.Context(), op)
	if err != nil || !seeded {
		t.Fatalf("ensureManagedRuntimeDesiredRunning = (%v, %v), want (true, nil)", seeded, err)
	}
	reconciler := runtimeservice.NewReconciler(app.runtime, app.desiredStateStore(), runtimeservice.DefaultReconcileInterval, nil)
	if err := reconciler.ReconcileOnce(t.Context()); err != nil {
		t.Fatalf("ReconcileOnce after restart = %v", err)
	}
	if client.starts != 1 {
		t.Fatalf("Supervisor starts after restart = %d, want 1", client.starts)
	}

	// Once an operator explicitly stops the Runtime, a later restart must not
	// overwrite desired=stopped back to running.
	if _, err := app.runtimeSettings.SetEmbeddedRuntimeDesiredLifecycle(t.Context(), model.EmbeddedRuntimeDesiredStopped); err != nil {
		t.Fatal(err)
	}
	seededAgain, err := app.ensureManagedRuntimeDesiredRunning(t.Context(), op)
	if err != nil || seededAgain {
		t.Fatalf("ensureManagedRuntimeDesiredRunning after explicit stop = (%v, %v), want (false, nil)", seededAgain, err)
	}
	if err := reconciler.ReconcileOnce(t.Context()); err != nil {
		t.Fatalf("ReconcileOnce while stopped = %v", err)
	}
	if client.starts != 1 {
		t.Fatalf("Supervisor restarted an explicitly stopped runtime: starts=%d", client.starts)
	}
}

func TestIncompleteManagedInstallAutoStartsAfterRestartAndCompletesAcrossGenerations(t *testing.T) {
	incompleteManaged := func(r *freshstore.Record, env managedInstallEnv) {
		completedManagedInstall(r, env)
		r.Operation.ProductComplete = false
	}
	harness := newManagedInstallHarnessWith(t, incompleteManaged, true)
	app := harness.app

	artifactMode := "ready"
	artifactID := string(managedInstallRuntimeStatus().ActiveGatewayArtifact.ArtifactID)
	otherArtifactID := "sha256:" + strings.Repeat("f", 64)
	artifactServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer artifact-test-token" || r.URL.Path != "/v1/runtime/artifact" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch artifactMode {
		case "missing":
			_, _ = w.Write([]byte(`{"schemaVersion":1,"pinned":{"version":"8.0.55","artifactId":"` + artifactID + `"},"active":null,"required":true,"operation":null}`))
		case "mismatch":
			_, _ = w.Write([]byte(`{"schemaVersion":1,"pinned":{"version":"8.0.55","artifactId":"` + otherArtifactID + `"},"active":{"artifactId":"` + artifactID + `","origin":"acquired"},"required":false,"operation":null}`))
		case "recovery":
			_, _ = w.Write([]byte(`{"schemaVersion":1,"pinned":{"version":"8.0.55","artifactId":"` + artifactID + `"},"active":{"artifactId":"` + artifactID + `","origin":"acquired"},"required":false,"operation":{"id":"op-1","source":"upload","phase":"failed","receivedBytes":1,"totalBytes":1,"code":"recovery_required"}}`))
		default:
			_, _ = w.Write([]byte(`{"schemaVersion":1,"pinned":{"version":"8.0.55","artifactId":"` + artifactID + `"},"active":{"artifactId":"` + artifactID + `","origin":"acquired"},"required":false,"operation":null}`))
		}
	}))
	defer artifactServer.Close()
	app.artifact = runtimeservice.NewArtifactClient(artifactServer.URL, "artifact-test-token")

	restarted := managedInstallRuntimeStatus()
	restarted.Generation = 991234
	restarted.State = model.RuntimeStateOffline
	restarted.Capabilities = model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStop}
	restarted.Recovery = &model.RuntimeRecoveryObservation{State: model.RuntimeRecoveryStateInactive}
	client := &freshRuntimeTestClient{status: restarted}
	app.runtime = client
	app.runtimeExternal = nil
	app.keyApply = freshkeyapply.NewManaged(app.keyRepo, app.keys, client, harness.endpoint, app.managedSource)

	reconciler := runtimeservice.NewReconciler(app.runtime, app.desiredStateStore(), runtimeservice.DefaultReconcileInterval, nil)

	// Non-ready #1166 artifact states must never trigger Start.
	for _, blocked := range []string{"missing", "mismatch", "recovery"} {
		artifactMode = blocked
		if err := reconciler.ReconcileOnce(t.Context()); err != nil {
			t.Fatalf("ReconcileOnce(%s) = %v", blocked, err)
		}
		if client.starts != 0 {
			t.Fatalf("Supervisor started while artifact was %s: starts=%d", blocked, client.starts)
		}
	}

	// ConnectExternal checkpoints must never auto-start local Supervisor.
	beforeExternal, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	externalRec, err := app.store.Update(t.Context(), beforeExternal.Operation.Identity, beforeExternal.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.ConnectExternal, Stage: "committed",
			TargetIdentity: freshkeyapply.ExternalIdentity(harness.endpoint), TargetGeneration: 1,
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	artifactMode = "ready"
	if err := reconciler.ReconcileOnce(t.Context()); err != nil {
		t.Fatalf("ReconcileOnce(external) = %v", err)
	}
	if client.starts != 0 {
		t.Fatalf("Supervisor started for ConnectExternal: starts=%d", client.starts)
	}

	// Restore the incomplete NewManaged committed checkpoint.
	if _, err := app.store.Update(t.Context(), externalRec.Operation.Identity, externalRec.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = beforeExternal.Operation.Strategy
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// With engine Ready and destination committed, Reconciler starts offline CPA
	// without writing runtime_desired_v1 into settings before ProductComplete.
	if err := reconciler.ReconcileOnce(t.Context()); err != nil {
		t.Fatalf("ReconcileOnce(incomplete managed) = %v", err)
	}
	if client.starts != 1 {
		t.Fatalf("Supervisor starts for incomplete managed = %d, want 1", client.starts)
	}
	// The automatic Start carries the artifact the checkpoint committed, so it
	// refuses to launch a selection the installation never staged.
	if got, want := client.startRequests[0].ExpectedActiveArtifactID, model.RuntimeArtifactID("sha256:"+strings.Repeat("a", 64)); got != want {
		t.Fatalf("automatic Start expected artifact = %q, want %q", got, want)
	}
	if _, found, err := app.runtimeSettings.LoadEmbeddedRuntimeDesiredState(t.Context()); err != nil || found {
		t.Fatalf("runtime_desired_v1 persisted before ProductComplete: found=%v err=%v", found, err)
	}
	if _, err := app.store.Load(t.Context()); err != nil {
		t.Fatalf("Fresh store became unloadable before completion: %v", err)
	}

	// Once CPA reaches Ready on the new Supervisor generation, completion must
	// still fail closed if destination client authentication is missing, and
	// succeed once proven, seeding durable runtime_desired_v1 = running.
	client.status.State = model.RuntimeStateReady
	savedKeys := harness.destination.installed()
	harness.destination.seed()
	unprovenComplete := installationAPI(t, app, http.MethodPost, "complete", strategyTestAdmin)
	if unprovenComplete.Code != http.StatusServiceUnavailable {
		t.Fatalf("complete with unproven destination across generation = %d, want 503", unprovenComplete.Code)
	}
	harness.destination.seed(savedKeys...)
	completed := installationAPI(t, app, http.MethodPost, "complete", strategyTestAdmin)
	if completed.Code != http.StatusOK {
		t.Fatalf("complete after restart generation rotation = %d %s", completed.Code, completed.Body.String())
	}
	desired, found, err := app.runtimeSettings.LoadEmbeddedRuntimeDesiredState(t.Context())
	if err != nil || !found || desired.DesiredLifecycle != model.EmbeddedRuntimeDesiredRunning {
		t.Fatalf("durable desired state after completion = %+v found=%v err=%v", desired, found, err)
	}
}

func TestIncompleteManagedDesiredStateSurfacesManagedSourceFailure(t *testing.T) {
	incompleteManaged := func(r *freshstore.Record, env managedInstallEnv) {
		completedManagedInstall(r, env)
		r.Operation.ProductComplete = false
	}
	harness := newManagedInstallHarnessWith(t, incompleteManaged, true)
	app := harness.app
	offlineAfterRestart := managedInstallRuntimeStatus()
	offlineAfterRestart.State = model.RuntimeStateOffline
	offlineAfterRestart.Capabilities = model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStop}
	offlineAfterRestart.Recovery = &model.RuntimeRecoveryObservation{
		State:             model.RuntimeRecoveryStateInactive,
		AttemptsRemaining: 0,
	}
	client := &freshRuntimeTestClient{status: offlineAfterRestart}
	app.runtime = client
	app.runtimeExternal = nil
	app.artifact = nil

	realManagedSource := app.managedSource
	var failOnce sync.Once
	app.managedSource = func(ctx context.Context) (freshkeyapply.ManagedInstall, error) {
		var transientErr error
		failOnce.Do(func() {
			transientErr = errors.New("sqlite busy on second read")
		})
		if transientErr != nil {
			return freshkeyapply.ManagedInstall{}, transientErr
		}
		return realManagedSource(ctx)
	}

	reconciler := runtimeservice.NewReconciler(app.runtime, app.desiredStateStore(), runtimeservice.DefaultReconcileInterval, nil)
	if err := reconciler.ReconcileOnce(t.Context()); err == nil {
		t.Fatal("ReconcileOnce with transient managedSource failure succeeded; want error")
	}
	if client.starts != 0 {
		t.Fatalf("Supervisor starts during managedSource failure = %d, want 0", client.starts)
	}
	if err := reconciler.ReconcileOnce(t.Context()); err != nil {
		t.Fatalf("ReconcileOnce after transient managedSource failure recovered = %v", err)
	}
	if client.starts != 1 {
		t.Fatalf("Supervisor starts after managedSource recovery = %d, want 1", client.starts)
	}
}

type hookedDesiredStateStore struct {
	runtimeservice.DesiredStateStore
	onLoad func(call int, desired model.EmbeddedRuntimeDesiredState, found bool, err error)
	mu     sync.Mutex
	calls  int
}

func (s *hookedDesiredStateStore) LoadEmbeddedRuntimeDesiredState(ctx context.Context) (model.EmbeddedRuntimeDesiredState, bool, error) {
	desired, found, err := s.DesiredStateStore.LoadEmbeddedRuntimeDesiredState(ctx)
	s.mu.Lock()
	s.calls++
	call := s.calls
	onLoad := s.onLoad
	s.mu.Unlock()
	if onLoad != nil {
		onLoad(call, desired, found, err)
	}
	return desired, found, err
}

func (s *hookedDesiredStateStore) LockEmbeddedRuntimeLifecycle(ctx context.Context) (func(), error) {
	if locker, ok := s.DesiredStateStore.(runtimeservice.LifecycleLocker); ok {
		return locker.LockEmbeddedRuntimeLifecycle(ctx)
	}
	return func() {}, nil
}

func (s *hookedDesiredStateStore) LoadEmbeddedRuntimeCommittedArtifact(ctx context.Context) (model.RuntimeArtifactID, bool, error) {
	if authority, ok := s.DesiredStateStore.(runtimeservice.CommittedArtifactStore); ok {
		return authority.LoadEmbeddedRuntimeCommittedArtifact(ctx)
	}
	return "", false, nil
}

func TestOperatorStopSerializesWithConcurrentReconcileAcrossBothWindows(t *testing.T) {
	t.Run("window 1: reconciler pre-reads running before operator Stop is submitted and acknowledged", func(t *testing.T) {
		app, op := completedRuntimeTestApplication(t)
		client := &freshRuntimeTestClient{
			status: freshRuntimeReadyStatus(model.RuntimeCapabilityStart, model.RuntimeCapabilityStop),
		}
		app.runtime = client
		app.runtimeExternal = nil
		if seeded, err := app.ensureManagedRuntimeDesiredRunning(t.Context(), op); err != nil || !seeded {
			t.Fatalf("ensureManagedRuntimeDesiredRunning = (%v, %v), want (true, nil)", seeded, err)
		}

		hooked := &hookedDesiredStateStore{DesiredStateStore: app.desiredStateStore()}
		hooked.onLoad = func(call int, desired model.EmbeddedRuntimeDesiredState, found bool, err error) {
			if call != 1 {
				return
			}
			if err != nil || !found || desired.DesiredLifecycle != model.EmbeddedRuntimeDesiredRunning {
				t.Errorf("first desired read = (%+v, %v, %v), want running", desired, found, err)
				return
			}
			stopRecorder := httptest.NewRecorder()
			client.onStop = func() {
				client.mu.Lock()
				offline := freshRuntimeReadyStatus(model.RuntimeCapabilityStart, model.RuntimeCapabilityStop)
				offline.State = model.RuntimeStateOffline
				offline.Recovery = &model.RuntimeRecoveryObservation{State: model.RuntimeRecoveryStateInactive}
				client.status = offline
				client.mu.Unlock()
			}
			app.runtimeOperations(stopRecorder, freshRuntimeRequest(t, "stop", "stop-win-1"), op, "runtime/operations")
			if stopRecorder.Code != http.StatusOK {
				t.Errorf("operator Stop status = %d body=%s", stopRecorder.Code, stopRecorder.Body.String())
			}
		}

		reconciler := runtimeservice.NewReconciler(app.runtime, hooked, runtimeservice.DefaultReconcileInterval, nil)
		if err := reconciler.ReconcileOnce(t.Context()); err != nil {
			t.Fatalf("ReconcileOnce() error = %v", err)
		}
		if client.starts != 0 {
			t.Fatalf("Supervisor starts after acknowledged operator Stop in window 1 = %d, want 0", client.starts)
		}
	})

	t.Run("window 2: operator Stop succeeds at Supervisor before desired row is updated", func(t *testing.T) {
		app, op := completedRuntimeTestApplication(t)
		stopReachedSupervisor := make(chan struct{})
		reconcilePreReadOldRow := make(chan struct{})
		client := &freshRuntimeTestClient{
			status: freshRuntimeReadyStatus(model.RuntimeCapabilityStart, model.RuntimeCapabilityStop),
		}
		client.onStop = func() {
			client.mu.Lock()
			offline := freshRuntimeReadyStatus(model.RuntimeCapabilityStart, model.RuntimeCapabilityStop)
			offline.State = model.RuntimeStateOffline
			offline.Recovery = &model.RuntimeRecoveryObservation{State: model.RuntimeRecoveryStateInactive}
			client.status = offline
			client.mu.Unlock()
			close(stopReachedSupervisor)
			<-reconcilePreReadOldRow
		}
		app.runtime = client
		app.runtimeExternal = nil
		if seeded, err := app.ensureManagedRuntimeDesiredRunning(t.Context(), op); err != nil || !seeded {
			t.Fatalf("ensureManagedRuntimeDesiredRunning = (%v, %v), want (true, nil)", seeded, err)
		}

		hooked := &hookedDesiredStateStore{DesiredStateStore: app.desiredStateStore()}
		hooked.onLoad = func(call int, desired model.EmbeddedRuntimeDesiredState, found bool, err error) {
			if call == 1 && found && desired.DesiredLifecycle == model.EmbeddedRuntimeDesiredRunning {
				close(reconcilePreReadOldRow)
			}
		}
		reconciler := runtimeservice.NewReconciler(app.runtime, hooked, runtimeservice.DefaultReconcileInterval, nil)
		reconcileErrCh := make(chan error, 1)
		go func() {
			<-stopReachedSupervisor
			reconcileErrCh <- reconciler.ReconcileOnce(t.Context())
		}()

		stopRecorder := httptest.NewRecorder()
		app.runtimeOperations(stopRecorder, freshRuntimeRequest(t, "stop", "stop-win-2"), op, "runtime/operations")
		if stopRecorder.Code != http.StatusOK {
			t.Fatalf("operator Stop status = %d body=%s", stopRecorder.Code, stopRecorder.Body.String())
		}
		if err := <-reconcileErrCh; err != nil {
			t.Fatalf("concurrent ReconcileOnce() error = %v", err)
		}
		if client.starts != 0 {
			t.Fatalf("Supervisor starts after acknowledged operator Stop in window 2 = %d, want 0", client.starts)
		}
	})
}

func TestOperatorOperationRespectsCancellationAndPreservesMutationBudgetWhileWaitingOnReconcile(t *testing.T) {
	t.Run("cancelled HTTP request exits promptly without leaving a blocking accepted journal row", func(t *testing.T) {
		app, op := completedRuntimeTestApplication(t)
		reconcileEnteredLock := make(chan struct{})
		releaseReconcile := make(chan struct{})
		var statusOnce sync.Once
		client := &freshRuntimeTestClient{
			status: freshRuntimeReadyStatus(model.RuntimeCapabilityStart, model.RuntimeCapabilityStop),
			onStatus: func(ctx context.Context) {
				statusOnce.Do(func() {
					close(reconcileEnteredLock)
					select {
					case <-releaseReconcile:
					case <-ctx.Done():
					}
				})
			},
		}
		app.runtime = client
		app.runtimeExternal = nil
		if seeded, err := app.ensureManagedRuntimeDesiredRunning(t.Context(), op); err != nil || !seeded {
			t.Fatalf("ensureManagedRuntimeDesiredRunning = (%v, %v), want (true, nil)", seeded, err)
		}

		reconciler := runtimeservice.NewReconciler(app.runtime, app.desiredStateStore(), runtimeservice.DefaultReconcileInterval, nil)
		reconcileDone := make(chan error, 1)
		go func() {
			reconcileDone <- reconciler.ReconcileOnce(t.Context())
		}()
		<-reconcileEnteredLock

		reqCtx, cancelReq := context.WithCancel(t.Context())
		req := freshRuntimeRequest(t, "stop", "stop-cancel-while-reconciling").WithContext(reqCtx)
		cancelledRecorder := httptest.NewRecorder()
		handlerDone := make(chan struct{})
		go func() {
			defer close(handlerDone)
			app.runtimeOperations(cancelledRecorder, req, op, "runtime/operations")
		}()

		time.Sleep(25 * time.Millisecond)
		cancelReq()
		select {
		case <-handlerDone:
		case <-time.After(2 * time.Second):
			t.Fatal("cancelled operator Stop stayed blocked on reconcile lifecycle lock")
		}
		if cancelledRecorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("cancelled operator Stop status = %d body=%s, want 503", cancelledRecorder.Code, cancelledRecorder.Body.String())
		}

		journalData, err := app.runtimeJournal.load(t.Context())
		if err != nil {
			t.Fatalf("load runtime journal: %v", err)
		}
		if len(journalData.Operations) != 0 {
			t.Fatalf("cancelled operator Stop left orphan journal entries: %#v", journalData.Operations)
		}

		close(releaseReconcile)
		if err := <-reconcileDone; err != nil {
			t.Fatalf("ReconcileOnce() error = %v", err)
		}

		nextRecorder := httptest.NewRecorder()
		app.runtimeOperations(nextRecorder, freshRuntimeRequest(t, "stop", "stop-after-cancelled-waiter"), op, "runtime/operations")
		if nextRecorder.Code != http.StatusOK {
			t.Fatalf("follow-up operator Stop status = %d body=%s, want 200", nextRecorder.Code, nextRecorder.Body.String())
		}
	})

	t.Run("waiting operator request observes post-reconcile Status and receives full mutationContext budget", func(t *testing.T) {
		app, op := completedRuntimeTestApplication(t)
		reconcileEnteredLock := make(chan struct{})
		releaseReconcile := make(chan struct{})
		var statusOnce sync.Once
		var observedStopDeadline time.Time
		var observedStopHasDeadline bool
		var observedStopGeneration model.RuntimeGeneration

		client := &freshRuntimeTestClient{
			status: freshRuntimeReadyStatus(model.RuntimeCapabilityStart, model.RuntimeCapabilityStop),
			onStatus: func(ctx context.Context) {
				statusOnce.Do(func() {
					close(reconcileEnteredLock)
					select {
					case <-releaseReconcile:
					case <-ctx.Done():
					}
				})
			},
			onStopRequest: func(ctx context.Context, request model.RuntimeMutationRequest) {
				observedStopDeadline, observedStopHasDeadline = ctx.Deadline()
				observedStopGeneration = request.ExpectedRuntimeGeneration
			},
		}
		app.runtime = client
		app.runtimeExternal = nil
		if seeded, err := app.ensureManagedRuntimeDesiredRunning(t.Context(), op); err != nil || !seeded {
			t.Fatalf("ensureManagedRuntimeDesiredRunning = (%v, %v), want (true, nil)", seeded, err)
		}

		reconciler := runtimeservice.NewReconciler(app.runtime, app.desiredStateStore(), runtimeservice.DefaultReconcileInterval, nil)
		reconcileDone := make(chan error, 1)
		go func() {
			reconcileDone <- reconciler.ReconcileOnce(t.Context())
		}()
		<-reconcileEnteredLock

		stopRecorder := httptest.NewRecorder()
		stopDone := make(chan struct{})
		go func() {
			defer close(stopDone)
			app.runtimeOperations(stopRecorder, freshRuntimeRequest(t, "stop", "stop-after-brief-wait"), op, "runtime/operations")
		}()

		// Let the operator request wait on the lifecycle lock while reconcile
		// rotates the observed generation before releasing the lock.
		time.Sleep(40 * time.Millisecond)
		client.mu.Lock()
		rotated := freshRuntimeReadyStatus(model.RuntimeCapabilityStart, model.RuntimeCapabilityStop)
		rotated.Generation = 42
		client.status = rotated
		client.mu.Unlock()
		releaseAt := time.Now()
		close(releaseReconcile)

		if err := <-reconcileDone; err != nil {
			t.Fatalf("ReconcileOnce() error = %v", err)
		}
		<-stopDone
		if stopRecorder.Code != http.StatusOK {
			t.Fatalf("operator Stop status = %d body=%s, want 200", stopRecorder.Code, stopRecorder.Body.String())
		}
		if !observedStopHasDeadline {
			t.Fatal("mutationContext passed to Stop had no deadline")
		}
		if observedStopDeadline.Before(releaseAt.Add(freshRuntimeMutationBudget)) {
			t.Fatalf("mutationContext budget was consumed while waiting on lifecycle lock: deadline=%v want >= %v", observedStopDeadline, releaseAt.Add(freshRuntimeMutationBudget))
		}
		if observedStopGeneration != 42 {
			t.Fatalf("ExpectedRuntimeGeneration = %d, want post-lock generation 42", observedStopGeneration)
		}
	})
}
