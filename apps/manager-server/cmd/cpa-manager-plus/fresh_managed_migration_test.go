package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

type migrationRuntime struct {
	status     model.RuntimeObservedStatus
	stages     []model.RuntimeDestinationRequest
	starts     []model.RuntimeMutationRequest
	stageErr   error
	stageState model.RuntimeOperationState
	startState model.RuntimeOperationState
	stagedKey  string
}

func (r *migrationRuntime) Status(context.Context) (model.RuntimeObservedStatus, error) {
	return r.status, nil
}
func (r *migrationRuntime) Start(_ context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	r.starts = append(r.starts, request)
	state := r.startState
	if state == "" {
		state = model.RuntimeOperationSucceeded
	}
	return model.RuntimeOperationResult{OperationID: request.OperationID, OperationType: model.RuntimeOperationStart, RuntimeIdentity: r.status.Identity, RuntimeGeneration: r.status.Generation, State: state}, nil
}
func (r *migrationRuntime) Stop(context.Context, model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	return model.RuntimeOperationResult{}, context.Canceled
}
func (r *migrationRuntime) Restart(context.Context, model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	return model.RuntimeOperationResult{}, context.Canceled
}
func (r *migrationRuntime) PrepareUpdate(context.Context, model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
	return model.RuntimeOperationResult{}, context.Canceled
}
func (r *migrationRuntime) ActivateUpdate(context.Context, model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
	return model.RuntimeOperationResult{}, context.Canceled
}
func (r *migrationRuntime) ObserveUpdateOperation(context.Context, model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
	return model.RuntimeUpdateOperationObservation{}, context.Canceled
}
func (r *migrationRuntime) StageDestination(_ context.Context, request model.RuntimeDestinationRequest) (model.RuntimeOperationResult, error) {
	r.stages = append(r.stages, request)
	r.stagedKey = request.ManagementKey
	if r.stageErr != nil {
		return model.RuntimeOperationResult{}, r.stageErr
	}
	state := r.stageState
	if state == "" {
		state = model.RuntimeOperationSucceeded
	}
	return model.RuntimeOperationResult{OperationID: request.OperationID, OperationType: model.RuntimeOperationStageDestination, RuntimeIdentity: r.status.Identity, RuntimeGeneration: r.status.Generation, State: state}, nil
}

// installTestClientKey replays one canonical install Client Key for a default
// destination, exactly as the install authority does across a resume.
func installTestClientKey(context.Context, bootstrap.FreshOperation, uint64) (freshstrategy.InitialClientKey, error) {
	return freshstrategy.InitialClientKey{KeyID: strings.Repeat("9", 32), Secret: "sk-" + strings.Repeat("aB3", 21) + "Z"}, nil
}

func migrationFixture(t *testing.T, generation model.RuntimeGeneration, stageErr error) (*freshApplication, *migrationRuntime, freshstore.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manager.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fresh := freshstore.New(db)
	id := bootstrap.FreshIdentity{AdmissionID: "admission", InstallationID: "installation", Generation: "generation", OperationID: "migration-operation", DatabasePath: path}
	admin, err := security.NewAdminCredential("Cpamp_Migration_Admin_839216", "host")
	if err != nil {
		t.Fatal(err)
	}
	record, err := fresh.Create(t.Context(), id, &admin)
	if err != nil {
		t.Fatal(err)
	}
	artifact := model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64))
	runtime := &migrationRuntime{status: model.RuntimeObservedStatus{Identity: "runtime", Generation: generation, ProtocolVersion: "v1", State: model.RuntimeStateOffline, Capabilities: model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStageDestination}, ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: artifact}}, stageErr: stageErr}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v8/management/config" || runtime.stagedKey == "" || req.Header.Get("Authorization") != "Bearer "+runtime.stagedKey {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
	}))
	t.Cleanup(server.Close)
	protector, err := security.NewProtector([]byte("migration data key"))
	if err != nil {
		t.Fatal(err)
	}
	service := freshstrategy.New(freshstrategy.Options{Runtime: runtime, Protector: protector, DestinationBaseURL: server.URL, Qualification: bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true}, QualifiedArtifactID: artifact, InitialClientKey: installTestClientKey})
	// The probe is wired exactly as production wires it, so a test can observe that
	// a saved connection invalidates the previous plugin answer.
	app := &freshApplication{store: fresh, strategy: service, pluginCapability: newPluginCapabilityProbe(time.Hour, time.Minute, func(context.Context) bool { return true })}
	app.runtime = runtime
	app.protector = protector
	app.runtimeSettings = setting.New(db, protector)
	checkpoint := bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, Stage: "committed", TargetIdentity: string(runtime.status.Identity), TargetGeneration: 7, TargetArtifactID: string(artifact)}
	if _, err = fresh.Update(t.Context(), id, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = checkpoint
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: id.OperationID, Revision: 1, Target: "/console", Stage: bootstrap.PrefixCommitted, Committed: true}
		r.Operation.ProductComplete = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return app, runtime, fresh
}

func TestManagedDestinationMigrationIsIdempotentAndJournaled(t *testing.T) {
	app, runtime, fresh := migrationFixture(t, 7, nil)
	if err := app.migrateManagedDestination(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(runtime.stages) != 1 || len(runtime.starts) != 1 {
		t.Fatalf("migration operations = %d/%d", len(runtime.stages), len(runtime.starts))
	}
	record, err := fresh.Load(t.Context())
	if err != nil || record.Connection == nil || record.Operation.Strategy.DestinationConnectionIdentity == "" || record.AdoptionCiphertext == "" {
		t.Fatalf("migration record incomplete: connection=%v strategy=%+v err=%v", record.Connection != nil, record.Operation.Strategy, err)
	}
	if err := app.migrateManagedDestination(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(runtime.stages) != 1 || len(runtime.starts) != 1 {
		t.Fatal("completed migration was not idempotent")
	}
}

func TestManagedDestinationMigrationRefusesGenerationAndGatewayAuthDrift(t *testing.T) {
	app, runtime, _ := migrationFixture(t, 8, nil)
	if err := app.migrateManagedDestination(t.Context()); err == nil {
		t.Fatal("generation drift was silently rebound")
	}
	if len(runtime.stages) != 0 {
		t.Fatal("generation drift staged a destination")
	}
	app, runtime, _ = migrationFixture(t, 7, &runtimeservice.ProtocolError{Code: runtimeservice.ProtocolErrorCode("gateway_auth_present"), HTTPStatus: http.StatusConflict})
	if err := app.migrateManagedDestination(t.Context()); err == nil {
		t.Fatal("non-empty Gateway auth was silently adopted")
	}
	if len(runtime.starts) != 0 {
		t.Fatal("auth refusal started Runtime")
	}
}

// The one-time migration retries through the same durable attempt: a terminal
// Supervisor failure is persisted before the startup retries with a new
// operation ID, instead of replaying the recorded failure forever.
func TestManagedDestinationMigrationRetiresTerminalStageFailure(t *testing.T) {
	app, runtime, fresh := migrationFixture(t, 7, nil)
	runtime.stageState = model.RuntimeOperationFailed
	if err := app.migrateManagedDestination(t.Context()); err == nil {
		t.Fatal("terminal stage failure was reported as a completed migration")
	}
	record, err := fresh.Load(t.Context())
	if err != nil || record.Operation.Strategy.DestinationAttempt != 1 || record.Connection != nil ||
		record.Operation.Strategy.Stage != "committed" || record.Operation.Strategy.DestinationConnectionIdentity != "" || record.AdoptionCiphertext == "" {
		t.Fatalf("retired migration attempt: %+v %v", record.Operation.Strategy, err)
	}
	runtime.stageState = ""
	if err := app.migrateManagedDestination(t.Context()); err != nil {
		t.Fatal(err)
	}
	base := string(record.Operation.Identity.OperationID) + ":managed-migration-destination"
	if len(runtime.stages) != 2 || runtime.stages[0].OperationID != base || runtime.stages[1].OperationID != base+":attempt-1" {
		t.Fatalf("migration stage IDs = %+v", runtime.stages)
	}
	if len(runtime.starts) != 1 || runtime.starts[0].OperationID != base+":attempt-1:start" {
		t.Fatalf("migration start IDs = %+v", runtime.starts)
	}
	record, err = fresh.Load(t.Context())
	if err != nil || record.Connection == nil || record.Operation.Strategy.Stage != "committed" || record.Operation.Strategy.DestinationConnectionIdentity == "" {
		t.Fatalf("migration retry did not commit: %+v %v", record, err)
	}
	if err := app.migrateManagedDestination(t.Context()); err != nil || len(runtime.stages) != 2 {
		t.Fatalf("completed migration was not idempotent: %v", err)
	}
}

// A migration that saves a connection must invalidate the plugin capability: the
// answer describes the connection that was just replaced, and serving it for up
// to a TTL would advertise a capability the new connection may not have.
func TestManagedDestinationMigrationForgetsThePluginCapability(t *testing.T) {
	app, _, _ := migrationFixture(t, 7, nil)
	// Prove the old connection first, so the stale answer is observable.
	app.pluginCapability.launch = func(refresh func()) { refresh() }
	// A read serves the value known before it, so the proof is visible on the
	// following read.
	_ = app.pluginCapability.capable(t.Context())
	if !app.pluginCapability.capable(t.Context()) {
		t.Fatal("the pre-migration proof did not land")
	}
	if err := app.migrateManagedDestination(t.Context()); err != nil {
		t.Fatal(err)
	}
	if app.pluginCapability.capable(t.Context()) {
		t.Fatal("the plugin capability survived a saved connection change")
	}
}

func TestPrepareCompletedStartupMigratesWhileOfflineBeforeSeedingDesiredState(t *testing.T) {
	// 1. Legacy unmigrated start-only new_managed store: migration runs first while
	// Supervisor is Offline, writes DestinationConnectionIdentity, reloads op, and
	// only then seeds runtime_desired_v1 = running.
	app, runtime, fresh := migrationFixture(t, 7, nil)
	before, err := fresh.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	migratedOp := app.prepareCompletedStartup(t.Context(), before.Operation)
	if len(runtime.stages) != 1 || len(runtime.starts) != 1 {
		t.Fatalf("startup migration stages=%d starts=%d, want 1/1", len(runtime.stages), len(runtime.starts))
	}
	if !strings.HasPrefix(migratedOp.Strategy.DestinationConnectionIdentity, "managed:sha256:") {
		t.Fatalf("reloaded startup operation missing DestinationConnectionIdentity: %+v", migratedOp.Strategy)
	}
	desired, found, err := app.runtimeSettings.LoadEmbeddedRuntimeDesiredState(t.Context())
	if err != nil || !found || desired.DesiredLifecycle != model.EmbeddedRuntimeDesiredRunning {
		t.Fatalf("post-migration desired state = %+v found=%v err=%v", desired, found, err)
	}

	// 2. When migration is blocked (e.g. generation drift), DestinationConnectionIdentity
	// remains empty and runtime_desired_v1 stays unseeded so Reconciler never starts
	// an unmigrated CPA.
	blockedApp, _, blockedFresh := migrationFixture(t, 8, nil)
	blockedRecord, err := blockedFresh.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_ = blockedApp.prepareCompletedStartup(t.Context(), blockedRecord.Operation)
	if _, found, err := blockedApp.runtimeSettings.LoadEmbeddedRuntimeDesiredState(t.Context()); err != nil || found {
		t.Fatalf("blocked migration seeded desired state: found=%v err=%v", found, err)
	}

	// 3. Historical operator-managed connection (Connection != nil, empty
	// DestinationConnectionIdentity) stays unmigrated and unseeded.
	operatorApp, _, operatorFresh := migrationFixture(t, 7, nil)
	opRecord, err := operatorFresh.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := operatorApp.runtimeSettings.SaveManagerConfig(t.Context(), model.ManagerConfig{
		CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: "http://external-cpa:8317", ManagementKey: "operator-key"},
	}); err != nil {
		t.Fatal(err)
	}
	_ = operatorApp.prepareCompletedStartup(t.Context(), opRecord.Operation)
	if _, found, err := operatorApp.runtimeSettings.LoadEmbeddedRuntimeDesiredState(t.Context()); err != nil || found {
		t.Fatalf("operator-managed connection seeded desired state: found=%v err=%v", found, err)
	}
}
