package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

type retryFixture struct {
	app     *freshApplication
	runtime *migrationRuntime
	request freshstrategy.Request
	record  freshstore.Record
}

// newRetryFixture opens one Fresh installation and commits the staged managed
// destination checkpoint, which is the state a browser retry starts from.
func newRetryFixture(t *testing.T) *retryFixture {
	t.Helper()
	ctx := t.Context()
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(ctx, paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	artifact := model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64))
	runtime := &migrationRuntime{status: model.RuntimeObservedStatus{Identity: "runtime", Generation: 3, ProtocolVersion: "v1", State: model.RuntimeStateReady,
		Capabilities:          model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStageDestination},
		ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: artifact}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v8/management/config" || runtime.stagedKey == "" || r.Header.Get("Authorization") != "Bearer "+runtime.stagedKey {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
	}))
	t.Cleanup(server.Close)
	app := newFreshApplication(opened, config.Config{}, nil)
	app.strategy = freshstrategy.New(freshstrategy.Options{Runtime: runtime, Protector: opened.Protector, DestinationBaseURL: server.URL, InitialClientKey: installTestClientKey,
		Qualification: bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true}, QualifiedArtifactID: artifact})
	request := freshstrategy.Request{Strategy: bootstrap.NewManaged, ExpectedRuntimeIdentity: "runtime", ExpectedRuntimeGeneration: 3, ExpectedArtifactID: artifact}
	record, err := app.store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := app.strategy.Execute(ctx, record.Operation, request)
	if err != nil || staged.AdoptionCiphertext == "" || staged.Checkpoint.Stage != "staged" {
		t.Fatalf("stage: %+v %v", staged, err)
	}
	record, err = app.store.Update(ctx, record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = staged.Checkpoint
		r.AdoptionCiphertext = staged.AdoptionCiphertext
		// A real installation commits its public route before it commits the
		// strategy; the browser endpoint below only exists behind that route.
		r.Operation.Routing.OperationID = r.Operation.Identity.OperationID
		r.Operation.Routing.Revision = 1
		r.Operation.Routing.Target = "/cpamp-admin"
		r.Operation.Routing.InferenceTarget = "/cpamp-api"
		r.Operation.Routing.Stage = bootstrap.PrefixCommitted
		r.Operation.Routing.Committed = true
		r.Operation.Routing.InferenceCommitted = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return &retryFixture{app: app, runtime: runtime, request: request, record: record}
}

func (f *retryFixture) strategy(t *testing.T, revision uint64) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(struct {
		Identity bootstrap.FreshIdentity `json:"identity"`
		Revision uint64                  `json:"expectedRevision"`
		freshstrategy.Request
	}{f.record.Operation.Identity, revision, f.request})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, currentInstallManagementAPIV2Prefix+"strategy", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	response := httptest.NewRecorder()
	f.app.canonicalAPI(response, request)
	return response
}

// A terminal Supervisor stage failure must be durable before the browser sees
// the blocker, and the next request must re-issue the destination under the next
// operation ID. Advancing twice, or not at all, both leave the install stuck.
func TestFreshStrategyRetiresATerminalDestinationFailureInTheSameCAS(t *testing.T) {
	fixture := newRetryFixture(t)
	before := fixture.record
	fixture.runtime.stageState = model.RuntimeOperationFailed
	response := fixture.strategy(t, before.Operation.Revision)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "managed_destination_stage_failed") {
		t.Fatalf("terminal stage failure: %d %s", response.Code, response.Body.String())
	}
	after, err := fixture.app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.Operation.Revision != before.Operation.Revision+1 || after.Operation.Strategy.DestinationAttempt != 1 ||
		after.Operation.Strategy.Stage != "staged" || after.AdoptionCiphertext != before.AdoptionCiphertext || after.Connection != nil {
		t.Fatalf("retired attempt: revision=%d strategy=%+v", after.Operation.Revision, after.Operation.Strategy)
	}
	if after.Operation.Strategy.DestinationConnectionIdentity != before.Operation.Strategy.DestinationConnectionIdentity {
		t.Fatal("retirement rebound the destination identity")
	}
	// A stale retry must not fork a second attempt from the same revision.
	if replay := fixture.strategy(t, before.Operation.Revision); replay.Code == http.StatusOK {
		t.Fatalf("stale revision was accepted: %s", replay.Body.String())
	}
	stale, err := fixture.app.store.Load(t.Context())
	if err != nil || stale.Operation.Strategy.DestinationAttempt != 1 || stale.Operation.Revision != after.Operation.Revision {
		t.Fatalf("stale retry advanced the attempt: %+v %v", stale.Operation.Strategy, err)
	}
	// The next request at the new revision completes the install.
	fixture.record = after
	fixture.runtime.stageState = ""
	if next := fixture.strategy(t, after.Operation.Revision); next.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", next.Code, next.Body.String())
	}
	committed, err := fixture.app.store.Load(t.Context())
	if err != nil || committed.Connection == nil || committed.Operation.Strategy.Stage != "committed" {
		t.Fatalf("retry did not commit: %+v %v", committed.Operation.Strategy, err)
	}
	base := string(after.Operation.Identity.OperationID) + ":managed-destination"
	if len(fixture.runtime.stages) != 2 || fixture.runtime.stages[1].OperationID != base+":attempt-1" {
		t.Fatalf("destination operation IDs = %+v", fixture.runtime.stages)
	}
	if len(fixture.runtime.starts) != 1 || fixture.runtime.starts[0].OperationID != base+":attempt-1:start" {
		t.Fatalf("start operation IDs = %+v", fixture.runtime.starts)
	}
}

// An unresolved destination never advances the attempt, so no second operation
// can race one the Supervisor may still be running.
func TestFreshStrategyKeepsTheAttemptWhenTheDestinationIsUnresolved(t *testing.T) {
	fixture := newRetryFixture(t)
	before := fixture.record
	fixture.runtime.startState = model.RuntimeOperationRunning
	response := fixture.strategy(t, before.Operation.Revision)
	if response.Code != http.StatusOK {
		t.Fatalf("running start: %d %s", response.Code, response.Body.String())
	}
	after, err := fixture.app.store.Load(t.Context())
	if err != nil || after.Operation.Strategy.DestinationAttempt != 0 || after.Operation.Strategy.Stage != "starting" {
		t.Fatalf("running start checkpoint: %+v %v", after.Operation.Strategy, err)
	}
	base := string(after.Operation.Identity.OperationID) + ":managed-destination"
	for _, stage := range fixture.runtime.stages {
		if stage.OperationID != base {
			t.Fatalf("unresolved attempt forked a destination: %q", stage.OperationID)
		}
	}
	for _, start := range fixture.runtime.starts {
		if start.OperationID != base+":start" {
			t.Fatalf("unresolved attempt forked a start: %q", start.OperationID)
		}
	}
}
