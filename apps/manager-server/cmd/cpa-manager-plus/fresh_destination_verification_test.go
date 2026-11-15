package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

type verificationFixture struct {
	app      *freshApplication
	runtime  *migrationRuntime
	request  freshstrategy.Request
	record   freshstore.Record
	readable atomic.Bool
	base     string
}

// newVerificationFixture stages one managed destination and answers the
// destination read only while readable is set. It is the transport-level state a
// browser resume starts from after a typed
// managed_destination_verification_required refusal.
func newVerificationFixture(t *testing.T) *verificationFixture {
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
	runtime := &migrationRuntime{status: model.RuntimeObservedStatus{
		Identity: "runtime", Generation: 3, ProtocolVersion: "v1", State: model.RuntimeStateReady,
		Capabilities:          model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStageDestination},
		ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: artifact},
	}}
	fixture := &verificationFixture{runtime: runtime}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !fixture.readable.Load() || r.URL.Path != "/v8/management/config" ||
			runtime.stagedKey == "" || r.Header.Get("Authorization") != "Bearer "+runtime.stagedKey {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
	}))
	t.Cleanup(server.Close)
	app := newFreshApplication(opened, config.Config{}, nil)
	app.strategy = freshstrategy.New(freshstrategy.Options{
		Runtime: runtime, Protector: opened.Protector, DestinationBaseURL: server.URL,
		Qualification:       bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true},
		QualifiedArtifactID: artifact, InitialClientKey: installTestClientKey,
	})
	request := freshstrategy.Request{
		Strategy: bootstrap.NewManaged, ExpectedRuntimeIdentity: "runtime",
		ExpectedRuntimeGeneration: 3, ExpectedArtifactID: artifact,
	}
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
	fixture.app, fixture.request, fixture.record = app, request, record
	fixture.base = string(record.Operation.Identity.OperationID) + ":managed-destination"
	// The staged intent itself is not the refused attempt's write.
	runtime.stages, runtime.starts = nil, nil
	return fixture
}

func (f *verificationFixture) strategy(t *testing.T, revision uint64) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(struct {
		Identity bootstrap.FreshIdentity `json:"identity"`
		Revision uint64                  `json:"expectedRevision"`
		freshstrategy.Request
	}{f.record.Operation.Identity, revision, f.request})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, currentInstallManagementAPIV2Prefix+"strategy", strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	response := httptest.NewRecorder()
	f.app.canonicalAPI(response, request)
	return response
}

// A typed managed_destination_verification_required refusal records no
// checkpoint, so a resume must replay the same Fresh operation under the same
// fixed Stage and Start identity. A refusal that forked a second Supervisor
// operation, or a resume that wrote a new checkpoint, would both leave the
// wizard unable to tell a retry from a duplicate install.
func TestDestinationVerificationRefusalLetsTheResumeReuseTheSameAttempt(t *testing.T) {
	fixture := newVerificationFixture(t)
	before := fixture.record

	refused := fixture.strategy(t, before.Operation.Revision)
	if refused.Code != http.StatusConflict ||
		!strings.Contains(refused.Body.String(), "managed_destination_verification_required") {
		t.Fatalf("unverified destination = %d %s", refused.Code, refused.Body.String())
	}
	afterRefusal, err := fixture.app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if afterRefusal.Operation.Revision != before.Operation.Revision ||
		!reflect.DeepEqual(afterRefusal, before) {
		t.Fatalf("the refusal wrote a checkpoint: revision=%d strategy=%+v",
			afterRefusal.Operation.Revision, afterRefusal.Operation.Strategy)
	}
	refusedStages, refusedStarts := fixture.runtime.stages, fixture.runtime.starts
	if len(refusedStages) != 1 || refusedStages[0].OperationID != fixture.base {
		t.Fatalf("refused stage identity = %+v", refusedStages)
	}
	if len(refusedStarts) != 1 || refusedStarts[0].OperationID != fixture.base+":start" {
		t.Fatalf("refused start identity = %+v", refusedStarts)
	}

	// The destination becomes readable: the resume is the same operation, so it
	// replays that exact Stage/Start pair instead of a second one.
	fixture.readable.Store(true)
	resumed := fixture.strategy(t, before.Operation.Revision)
	if resumed.Code != http.StatusOK {
		t.Fatalf("resume = %d %s", resumed.Code, resumed.Body.String())
	}
	committed, err := fixture.app.store.Load(t.Context())
	if err != nil || committed.Connection == nil || committed.Operation.Strategy.Stage != "committed" {
		t.Fatalf("resume did not commit: %+v %v", committed.Operation.Strategy, err)
	}
	if committed.Operation.Strategy.DestinationAttempt != before.Operation.Strategy.DestinationAttempt {
		t.Fatalf("resume advanced the destination attempt: %d", committed.Operation.Strategy.DestinationAttempt)
	}
	if committed.Operation.Identity != before.Operation.Identity {
		t.Fatalf("resume changed the operation identity: %+v", committed.Operation.Identity)
	}
	if got := fixture.runtime.stages; len(got) != 2 ||
		got[0].OperationID != fixture.base || got[1].OperationID != fixture.base {
		t.Fatalf("resume forked a stage operation: %+v", got)
	}
	if got := fixture.runtime.starts; len(got) != 2 ||
		got[0].OperationID != fixture.base+":start" || got[1].OperationID != fixture.base+":start" {
		t.Fatalf("resume forked a start operation: %+v", got)
	}
	// The replayed calls are byte-identical apart from the point-in-time read, so
	// a Supervisor that only ever sees an idempotent key observes one attempt.
	if !reflect.DeepEqual(fixture.runtime.stages[0], fixture.runtime.stages[1]) {
		t.Fatalf("resume changed the stage request: %+v", fixture.runtime.stages)
	}
	if !reflect.DeepEqual(fixture.runtime.starts[0], fixture.runtime.starts[1]) {
		t.Fatalf("resume changed the start request: %+v", fixture.runtime.starts)
	}
	// The committing reply is the one delivery of the installation's Client Key.
	var delivered struct {
		InitialClientKey string `json:"initialClientKey"`
	}
	if err := json.Unmarshal(resumed.Body.Bytes(), &delivered); err != nil ||
		!model.ValidInitialClientKey(delivered.InitialClientKey) {
		t.Fatalf("committing reply did not deliver the install key (%v): %s", err, resumed.Body.String())
	}
	if resumed.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("install key reply is cacheable")
	}
	replay := fixture.strategy(t, committed.Operation.Revision)
	if replay.Code != http.StatusOK || strings.Contains(replay.Body.String(), delivered.InitialClientKey) {
		t.Fatalf("committed replay repeated the install key: %d %s", replay.Code, replay.Body.String())
	}
}

// A refusal that is never followed by a durable read stays exactly where it was:
// the store keeps the staged checkpoint and the operation stays resumable, so a
// lost reply is never mistaken for a completed install.
func TestDestinationVerificationRefusalStaysFrozenUntilTheRuntimeIsReadable(t *testing.T) {
	fixture := newVerificationFixture(t)
	before := fixture.record
	for attempt := range 3 {
		if response := fixture.strategy(t, before.Operation.Revision); response.Code != http.StatusConflict {
			t.Fatalf("attempt %d = %d %s", attempt, response.Code, response.Body.String())
		}
		current, err := fixture.app.store.Load(t.Context())
		if err != nil || !reflect.DeepEqual(current, before) {
			t.Fatalf("attempt %d changed the record: %+v %v", attempt, current.Operation.Strategy, err)
		}
	}
	if len(fixture.runtime.stages) != 3 {
		t.Fatalf("each refusal must replay the same stage, got %+v", fixture.runtime.stages)
	}
	for _, stage := range fixture.runtime.stages {
		if stage.OperationID != fixture.base {
			t.Fatalf("refusal forked a stage operation: %q", stage.OperationID)
		}
	}
	for _, start := range fixture.runtime.starts {
		if start.OperationID != fixture.base+":start" {
			t.Fatalf("refusal forked a start operation: %q", start.OperationID)
		}
	}
	// The staged checkpoint is what the wizard resumes from, and it was never
	// replaced by a refusal.
	if current, err := fixture.app.store.Load(t.Context()); err != nil ||
		current.Operation.Strategy.Stage != "staged" || current.AdoptionCiphertext == "" || current.Connection != nil {
		t.Fatalf("staged checkpoint after refusals: %+v %v", current.Operation.Strategy, err)
	}
}
