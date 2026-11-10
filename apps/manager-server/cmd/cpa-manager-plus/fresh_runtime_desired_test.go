package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

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
		record.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.NewManaged, Stage: "committed",
			TargetIdentity: "runtime-test", TargetGeneration: 7,
			TargetArtifactID: "sha256:" + strings.Repeat("a", 64),
		}
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
	if unmigratedSeeded, seedErr := app.ensureManagedRuntimeDesiredRunning(t.Context(), op); seedErr != nil || unmigratedSeeded {
		t.Fatalf("ensureManagedRuntimeDesiredRunning without destination identity = (%v, %v), want (false, nil)", unmigratedSeeded, seedErr)
	}
	op.Strategy.DestinationConnectionIdentity = "managed:sha256:" + strings.Repeat("b", 64)
	seeded, err := app.ensureManagedRuntimeDesiredRunning(t.Context(), op)
	if err != nil || !seeded {
		t.Fatalf("ensureManagedRuntimeDesiredRunning = (%v, %v), want (true, nil)", seeded, err)
	}
	reconciler := runtimeservice.NewReconciler(app.runtime, app.runtimeSettings, runtimeservice.DefaultReconcileInterval, nil)
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
