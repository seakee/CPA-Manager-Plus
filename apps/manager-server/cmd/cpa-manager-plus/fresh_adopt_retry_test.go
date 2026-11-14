package main

import (
	"bytes"
	"encoding/json"
	"errors"
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

const adoptTestSourceKey = "source-management-secret"

// errLostAdoptResponse is a Supervisor reply that never arrived: the caller saw
// a transport error, which is an unresolved result and never terminal evidence.
var errLostAdoptResponse = errors.New("lost stage response")

// adoptRetryFixture is a Fresh installation whose staged adoption checkpoint
// already holds the encrypted source inventory, which is the only state from
// which Execute mutates the destination. The target CPA it verifies against is
// the Manager-owned destination the Supervisor just staged.
type adoptRetryFixture struct {
	app          *freshApplication
	runtime      *migrationRuntime
	source       *httptest.Server
	sourceReads  int
	request      freshstrategy.Request
	record       freshstore.Record
	sourceClient []string
}

func acceptedClientAPIKeysDecision() []bootstrap.AdoptionResourceDecision {
	return []bootstrap.AdoptionResourceDecision{{
		ResourceRef: bootstrap.AdoptionRefClientAPIKeys, Kind: bootstrap.APIKeys, Scope: bootstrap.ClientAPIKeys,
		ReadEvidenceRef: "preflight:client-api-keys-read", ReplayEvidenceRef: "preflight:client-api-keys-replay",
		VerifyEvidenceRef: "preflight:client-api-keys-verify", Qualification: bootstrap.CapabilityQualified, Disposition: bootstrap.Migrate,
	}}
}

// adoptV8Source serves the bounded v8 safe read of a source with two client Keys
// and nothing else the adoption may migrate.
func adoptV8Source(t *testing.T, reads *int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v8/management/config" || r.Header.Get("Authorization") != "Bearer "+adoptTestSourceKey {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		*reads++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"source-auth"},"access":{"api-keys":["source-client-one","source-client-two"]}}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// adoptV8Destination is the staged destination CPA: it authenticates the
// Supervisor-generated management secret and serves exactly the staged Keys.
func adoptV8Destination(t *testing.T, runtime *migrationRuntime) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(runtime.stages) == 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		destination := runtime.stages[len(runtime.stages)-1]
		authorized := r.Header.Get("Authorization") == "Bearer "+destination.ManagementKey
		if r.URL.Path == "/v8/management/config" && r.Method == http.MethodGet {
			if !authorized {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			body, _ := json.Marshal(map[string]any{"config-version": 8, "server": map[string]int{"port": 8317},
				"oauth": map[string]string{"auth-dir": "destination-auth"}, "access": map[string]any{"api-keys": destination.APIKeys}})
			_, _ = w.Write(body)
			return
		}
		if r.URL.Path == "/v1/models" {
			for _, key := range destination.APIKeys {
				if r.Header.Get("Authorization") == "Bearer "+key {
					_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
					return
				}
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	return server
}

func newAdoptRetryFixture(t *testing.T) *adoptRetryFixture {
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
	fixture := &adoptRetryFixture{runtime: runtime}
	fixture.source = adoptV8Source(t, &fixture.sourceReads)
	target := adoptV8Destination(t, runtime)
	app := newFreshApplication(opened, config.Config{}, nil)
	app.strategy = freshstrategy.New(freshstrategy.Options{Runtime: runtime, Protector: opened.Protector, DestinationBaseURL: target.URL,
		Qualification:       bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true},
		QualifiedArtifactID: artifact, AdoptionResources: acceptedClientAPIKeysDecision()})
	request := freshstrategy.Request{Strategy: bootstrap.AdoptExisting, AdoptSourceClass: bootstrap.AdoptNetworkExisting,
		BaseURL: fixture.source.URL, ManagementKey: adoptTestSourceKey,
		ExpectedRuntimeIdentity: "runtime", ExpectedRuntimeGeneration: 3, ExpectedArtifactID: artifact}
	record, err := app.store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The first Execute reads the source and freezes the snapshot. It performs
	// no destination mutation, so the fixture starts exactly where a browser's
	// second confirmation starts.
	staged, err := app.strategy.Execute(ctx, record.Operation, request)
	if err != nil || staged.AdoptionCiphertext == "" || staged.Checkpoint.Stage != "staged" || len(runtime.stages) != 0 {
		t.Fatalf("stage: %+v %v", staged, err)
	}
	record, err = app.store.Update(ctx, record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = staged.Checkpoint
		r.AdoptionCiphertext = staged.AdoptionCiphertext
		r.Operation.Routing.OperationID = r.Operation.Identity.OperationID
		r.Operation.Routing.Revision = 1
		r.Operation.Routing.Target = "/cpamp-admin"
		r.Operation.Routing.Stage = bootstrap.PrefixCommitted
		r.Operation.Routing.Committed = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.app, fixture.request, fixture.record = app, request, record
	return fixture
}

func (f *adoptRetryFixture) strategy(t *testing.T, record freshstore.Record, request freshstrategy.Request) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(struct {
		Identity         bootstrap.FreshIdentity `json:"identity"`
		ExpectedRevision uint64                  `json:"expectedRevision"`
		freshstrategy.Request
	}{record.Operation.Identity, record.Operation.Revision, request})
	if err != nil {
		t.Fatal(err)
	}
	httpRequest := httptest.NewRequest(http.MethodPost, currentInstallManagementAPIV2Prefix+"strategy", bytes.NewReader(body))
	httpRequest.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	response := httptest.NewRecorder()
	f.app.canonicalAPI(response, httpRequest)
	return response
}

func (f *adoptRetryFixture) reload(t *testing.T) freshstore.Record {
	t.Helper()
	record, err := f.app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return record
}

// A terminal Supervisor failure must retire exactly one attempt under the same
// Fresh CAS, and the third terminal failure must stay a plain blocker: adoption
// is authorized for its original attempt plus one retry, never a loop.
func TestFreshAdoptRetiresExactlyOneDestinationAttempt(t *testing.T) {
	fixture := newAdoptRetryFixture(t)
	base := string(fixture.record.Operation.Identity.OperationID) + ":adopt-destination"
	fixture.runtime.stageState = model.RuntimeOperationFailed

	first := fixture.strategy(t, fixture.record, fixture.request)
	if first.Code != http.StatusConflict || !strings.Contains(first.Body.String(), "adoption_destination_stage_failed") {
		t.Fatalf("terminal stage failure: %d %s", first.Code, first.Body.String())
	}
	retired := fixture.reload(t)
	if retired.Operation.Revision != fixture.record.Operation.Revision+1 || retired.Operation.Strategy.DestinationAttempt != 1 ||
		retired.Operation.Strategy.Stage != "staged" || retired.AdoptionCiphertext != fixture.record.AdoptionCiphertext {
		t.Fatalf("first terminal failure was not retired exactly once: revision=%d %+v", retired.Operation.Revision, retired.Operation.Strategy)
	}

	// The second terminal failure is the retry's own evidence and is final.
	second := fixture.strategy(t, retired, fixture.request)
	if second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "adoption_destination_stage_failed") {
		t.Fatalf("retry terminal failure: %d %s", second.Code, second.Body.String())
	}
	final := fixture.reload(t)
	if final.Operation.Revision != retired.Operation.Revision || final.Operation.Strategy.DestinationAttempt != 1 {
		t.Fatalf("second terminal failure advanced the attempt: revision=%d %+v", final.Operation.Revision, final.Operation.Strategy)
	}
	if len(fixture.runtime.stages) != 2 || fixture.runtime.stages[1].OperationID != base+":attempt-1" {
		t.Fatalf("stage operation IDs = %+v", fixture.runtime.stages)
	}

	// The same bound holds at the store: no caller may commit a third attempt.
	if _, err := fixture.app.store.Update(t.Context(), final.Operation.Identity, final.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy.DestinationAttempt = bootstrap.MaxAdoptionDestinationRetry + 1
		return nil
	}); err == nil {
		t.Fatal("store accepted an attempt beyond the adoption retry bound")
	}
}

// The retry the terminal failure authorized must be able to commit, and the
// committed attempt is then final: replays reconcile it without any new
// Supervisor operation and the store refuses to advance past it.
func TestFreshAdoptRetryCommitsAndTheSuccessfulAttemptIsFinal(t *testing.T) {
	fixture := newAdoptRetryFixture(t)
	fixture.runtime.stageState = model.RuntimeOperationFailed
	if response := fixture.strategy(t, fixture.record, fixture.request); response.Code != http.StatusConflict {
		t.Fatalf("terminal failure: %d %s", response.Code, response.Body.String())
	}
	retired := fixture.reload(t)
	fixture.runtime.stageState = ""
	committed := fixture.strategy(t, retired, fixture.request)
	if committed.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", committed.Code, committed.Body.String())
	}
	done := fixture.reload(t)
	if done.Operation.Strategy.Stage != "committed" || done.Operation.Strategy.DestinationAttempt != 1 || done.Connection == nil {
		t.Fatalf("retry did not commit the second attempt: %+v", done.Operation.Strategy)
	}
	stages, starts := len(fixture.runtime.stages), len(fixture.runtime.starts)
	if replay := fixture.strategy(t, done, fixture.request); replay.Code != http.StatusOK {
		t.Fatalf("committed replay: %d %s", replay.Code, replay.Body.String())
	}
	if len(fixture.runtime.stages) != stages || len(fixture.runtime.starts) != starts {
		t.Fatal("committed replay issued a destination operation")
	}
	if _, err := fixture.app.store.Update(t.Context(), done.Operation.Identity, done.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy.DestinationAttempt = 0
		return nil
	}); err == nil {
		t.Fatal("store let a committed adoption be superseded by an earlier attempt")
	}
}

// A lost first response is not terminal evidence, so the attempt must not move;
// and the retry must present the frozen parameters, because a different source,
// secret or class after a lost reply is a different claim, not a replay.
func TestFreshAdoptLostResponseForbidsDifferentParameters(t *testing.T) {
	fixture := newAdoptRetryFixture(t)
	fixture.runtime.stageErr = errLostAdoptResponse
	first := fixture.strategy(t, fixture.record, fixture.request)
	if first.Code != http.StatusConflict || !strings.Contains(first.Body.String(), "adoption_destination_stage_unresolved") {
		t.Fatalf("lost stage reply: %d %s", first.Code, first.Body.String())
	}
	unresolved := fixture.reload(t)
	if unresolved.Operation.Strategy.DestinationAttempt != 0 || unresolved.Operation.Revision != fixture.record.Operation.Revision {
		t.Fatalf("lost reply advanced the attempt: %+v", unresolved.Operation.Strategy)
	}
	stages := len(fixture.runtime.stages)

	for name, mutate := range map[string]func(*freshstrategy.Request){
		"source":           func(r *freshstrategy.Request) { r.BaseURL = strings.Replace(r.BaseURL, "127.0.0.1", "localhost", 1) },
		"management key":   func(r *freshstrategy.Request) { r.ManagementKey = "different-source-secret" },
		"source class":     func(r *freshstrategy.Request) { r.AdoptSourceClass = bootstrap.AdoptLegacyManaged },
		"runtime identity": func(r *freshstrategy.Request) { r.ExpectedRuntimeIdentity = "other-runtime" },
	} {
		changed := fixture.request
		mutate(&changed)
		response := fixture.strategy(t, unresolved, changed)
		var blocker freshstrategy.Blocker
		if response.Code != http.StatusConflict || json.Unmarshal(response.Body.Bytes(), &blocker) != nil {
			t.Fatalf("%s: resend after a lost reply was not refused: %d %s", name, response.Code, response.Body.String())
		}
		if after := fixture.reload(t); after.Operation.Revision != unresolved.Operation.Revision {
			t.Fatalf("%s: refused resend mutated the Fresh record", name)
		}
	}
	// The only stage operation the Supervisor saw is the one whose reply was
	// lost; no refused resend raced a second destination.
	if len(fixture.runtime.stages) != stages {
		t.Fatalf("refused resends issued destination operations: %+v", fixture.runtime.stages)
	}
	// The identical parameters are still the same attempt, so the retry replays
	// attempt zero rather than forking a new operation.
	fixture.runtime.stageErr = nil
	if response := fixture.strategy(t, unresolved, fixture.request); response.Code != http.StatusOK {
		t.Fatalf("identical retry: %d %s", response.Code, response.Body.String())
	}
	base := string(unresolved.Operation.Identity.OperationID) + ":adopt-destination"
	for _, stage := range fixture.runtime.stages {
		if stage.OperationID != base {
			t.Fatalf("unresolved attempt forked a destination: %q", stage.OperationID)
		}
	}
}

// Admission: the Fresh claim session is the trust anchor, so a claim-authenticated
// network_existing adoption reaches the strategy adapter, while every other
// source class is refused with its own code before the source is read.
func TestFreshAdoptAdmissionKeepsOnlyTheLegacyRefusal(t *testing.T) {
	app, closeApp := newA3TestApp(t)
	defer closeApp()
	var sourceCalls int
	source := adoptV8Source(t, &sourceCalls)
	before, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		class   bootstrap.AdoptSourceClass
		blocker string
	}{
		{bootstrap.AdoptNetworkExisting, "adoption_destination_staging_required"},
		{bootstrap.AdoptLegacyManaged, "adoption_source_class_refused"},
		{bootstrap.AdoptSourceNone, "adoption_source_class_refused"},
	} {
		body, err := json.Marshal(struct {
			Identity         bootstrap.FreshIdentity `json:"identity"`
			ExpectedRevision uint64                  `json:"expectedRevision"`
			freshstrategy.Request
		}{before.Operation.Identity, before.Operation.Revision, freshstrategy.Request{
			Strategy: bootstrap.AdoptExisting, AdoptSourceClass: item.class, BaseURL: source.URL, ManagementKey: adoptTestSourceKey,
		}})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, currentInstallManagementAPIV2Prefix+"strategy", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		response := httptest.NewRecorder()
		app.canonicalAPI(response, request)
		var blocker freshstrategy.Blocker
		if response.Code != http.StatusConflict || json.Unmarshal(response.Body.Bytes(), &blocker) != nil || blocker.Code != item.blocker {
			t.Fatalf("adopt %q: %d %s", item.class, response.Code, response.Body.String())
		}
	}
	// Only the admitted class reaches the strategy adapter, and no attempt
	// mutated the Fresh record: this fixture has no destination Runtime, so the
	// adapter refuses before the source is read. Both refusals are typed and
	// side-effect free.
	if sourceCalls != 0 {
		t.Fatalf("source reads = %d", sourceCalls)
	}
	after, err := app.store.Load(t.Context())
	if err != nil || after.Operation.Revision != before.Operation.Revision || after.Operation.Strategy.Strategy != "" {
		t.Fatalf("admission mutated Fresh authority: %+v %v", after.Operation.Strategy, err)
	}
}

// A terminal destination start failure retires the attempt on exactly the same
// terms as a stage failure: the failed start is replaced under the next
// operation ID, and the second terminal start failure stays a plain blocker, so
// adoption never loops the Supervisor's terminal journal.
func TestFreshAdoptRetiresExactlyOneStartAttempt(t *testing.T) {
	fixture := newAdoptRetryFixture(t)
	fixture.runtime.startState = model.RuntimeOperationFailed
	base := string(fixture.record.Operation.Identity.OperationID) + ":adopt-destination"

	first := fixture.strategy(t, fixture.record, fixture.request)
	if first.Code != http.StatusConflict || !strings.Contains(first.Body.String(), "adoption_destination_start_failed") {
		t.Fatalf("terminal start failure: %d %s", first.Code, first.Body.String())
	}
	retired := fixture.reload(t)
	if retired.Operation.Revision != fixture.record.Operation.Revision+1 || retired.Operation.Strategy.DestinationAttempt != 1 ||
		retired.Operation.Strategy.Stage != "staged" || retired.AdoptionCiphertext != fixture.record.AdoptionCiphertext {
		t.Fatalf("first terminal start failure was not retired exactly once: revision=%d %+v", retired.Operation.Revision, retired.Operation.Strategy)
	}
	if len(fixture.runtime.stages) != 1 || fixture.runtime.stages[0].OperationID != base || len(fixture.runtime.starts) != 1 || fixture.runtime.starts[0].OperationID != base+":start" {
		t.Fatalf("first attempt operations = %+v %+v", fixture.runtime.stages, fixture.runtime.starts)
	}

	second := fixture.strategy(t, retired, fixture.request)
	if second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "adoption_destination_start_failed") {
		t.Fatalf("retry terminal start failure: %d %s", second.Code, second.Body.String())
	}
	final := fixture.reload(t)
	if final.Operation.Revision != retired.Operation.Revision || final.Operation.Strategy.DestinationAttempt != 1 || final.Operation.Strategy.Stage != "staged" {
		t.Fatalf("second terminal start failure advanced the attempt: revision=%d %+v", final.Operation.Revision, final.Operation.Strategy)
	}
	if len(fixture.runtime.stages) != 2 || fixture.runtime.stages[1].OperationID != base+":attempt-1" ||
		len(fixture.runtime.starts) != 2 || fixture.runtime.starts[1].OperationID != base+":attempt-1:start" {
		t.Fatalf("retry operations = %+v %+v", fixture.runtime.stages, fixture.runtime.starts)
	}
}

// Only terminal failure evidence may advance an adoption attempt. An accepted
// stage or a running start may still be executing in the Supervisor, so the
// Manager keeps attempt zero: a second destination would race the first.
func TestFreshAdoptUnresolvedDestinationKeepsTheAttempt(t *testing.T) {
	for _, item := range []struct {
		name    string
		stage   model.RuntimeOperationState
		start   model.RuntimeOperationState
		blocker string
	}{
		{name: "accepted stage", stage: model.RuntimeOperationAccepted, blocker: "adoption_destination_stage_unresolved"},
		{name: "running start", start: model.RuntimeOperationRunning, blocker: "adoption_destination_start_unresolved"},
	} {
		t.Run(item.name, func(t *testing.T) {
			fixture := newAdoptRetryFixture(t)
			fixture.runtime.stageState = item.stage
			fixture.runtime.startState = item.start
			response := fixture.strategy(t, fixture.record, fixture.request)
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), item.blocker) {
				t.Fatalf("%s: %d %s", item.name, response.Code, response.Body.String())
			}
			after := fixture.reload(t)
			if after.Operation.Revision != fixture.record.Operation.Revision || after.Operation.Strategy.DestinationAttempt != 0 || after.Operation.Strategy.Stage != "staged" || after.AdoptionCiphertext != fixture.record.AdoptionCiphertext {
				t.Fatalf("%s advanced the attempt: revision=%d %+v", item.name, after.Operation.Revision, after.Operation.Strategy)
			}
			base := string(after.Operation.Identity.OperationID) + ":adopt-destination"
			for _, stage := range fixture.runtime.stages {
				if stage.OperationID != base {
					t.Fatalf("%s forked a destination attempt: %q", item.name, stage.OperationID)
				}
			}
		})
	}
}

// A lost reply leaves attempt zero unresolved, and the resend must present the
// frozen protected snapshot. The source management key is only compared there,
// and the admitted class is unchanged, so the snapshot comparison itself is the
// guard that refuses a differently derived snapshot.
func TestFreshAdoptLostReplyRefusesADifferentSnapshot(t *testing.T) {
	fixture := newAdoptRetryFixture(t)
	fixture.runtime.stageErr = errLostAdoptResponse
	if response := fixture.strategy(t, fixture.record, fixture.request); response.Code != http.StatusConflict {
		t.Fatalf("lost stage reply: %d %s", response.Code, response.Body.String())
	}
	unresolved := fixture.reload(t)
	if unresolved.Operation.Strategy.DestinationAttempt != 0 || unresolved.Operation.Revision != fixture.record.Operation.Revision {
		t.Fatalf("lost reply advanced the attempt: %+v", unresolved.Operation.Strategy)
	}
	stages := len(fixture.runtime.stages)

	changed := fixture.request
	if changed.AdoptSourceClass != bootstrap.AdoptNetworkExisting {
		t.Fatalf("fixture class = %q", changed.AdoptSourceClass)
	}
	changed.ManagementKey = "different-source-secret"
	response := fixture.strategy(t, unresolved, changed)
	var blocker freshstrategy.Blocker
	if response.Code != http.StatusConflict || json.Unmarshal(response.Body.Bytes(), &blocker) != nil || blocker.Code != "fresh_strategy_owner_conflict" {
		t.Fatalf("different snapshot after a lost reply: %d %s", response.Code, response.Body.String())
	}
	after := fixture.reload(t)
	if after.Operation.Revision != unresolved.Operation.Revision || after.Operation.Strategy.DestinationAttempt != 0 || after.AdoptionCiphertext != unresolved.AdoptionCiphertext {
		t.Fatalf("refused different snapshot mutated the record: revision=%d %+v", after.Operation.Revision, after.Operation.Strategy)
	}
	if len(fixture.runtime.stages) != stages {
		t.Fatalf("refused different snapshot issued a destination operation: %+v", fixture.runtime.stages)
	}
}

// The compiled consumer record accepts legacy_managed, but an accepted
// consumer is qualification evidence, never an installation permit: admission
// still refuses that class before any source read or checkpoint mutation.
func TestFreshAdoptProductionQualificationCannotAdmitLegacyClass(t *testing.T) {
	paths := legacypreflight.DefaultPaths(filepath.Join(t.TempDir(), "manager.sqlite"))
	paths.Config = filepath.Join(filepath.Dir(paths.Database), "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opened.Close() }()
	app := newFreshApplication(opened, config.Config{}, nil)
	app.strategy = freshstrategy.NewProduction(freshstrategy.Options{Protector: opened.Protector})
	op := opened.Record.Operation
	if _, err = app.store.Update(t.Context(), op.Identity, op.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing.OperationID = op.Identity.OperationID
		r.Operation.Routing.Revision = 1
		r.Operation.Routing.Target = "/cpamp-admin"
		r.Operation.Routing.Stage = bootstrap.PrefixCommitted
		r.Operation.Routing.Committed = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	// The same production service, called directly, passes the qualification
	// gate for legacy_managed: the refusal below is admission, not qualification.
	legacy := freshstrategy.Request{Strategy: bootstrap.AdoptExisting, AdoptSourceClass: bootstrap.AdoptLegacyManaged,
		BaseURL: "http://127.0.0.1:8317", ManagementKey: adoptTestSourceKey}
	if _, err = app.strategy.Execute(t.Context(), before.Operation, legacy); err != nil {
		var blocker *freshstrategy.Blocker
		if !errors.As(err, &blocker) || blocker.Code != "adoption_destination_staging_required" {
			t.Fatalf("production qualification does not accept legacy_managed: %v", err)
		}
	} else {
		t.Fatal("production adoption unexpectedly executed without a destination runtime")
	}

	body, err := json.Marshal(struct {
		Identity         bootstrap.FreshIdentity `json:"identity"`
		ExpectedRevision uint64                  `json:"expectedRevision"`
		freshstrategy.Request
	}{before.Operation.Identity, before.Operation.Revision, legacy})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, currentInstallManagementAPIV2Prefix+"strategy", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	response := httptest.NewRecorder()
	app.canonicalAPI(response, request)
	var blocker freshstrategy.Blocker
	if response.Code != http.StatusConflict || json.Unmarshal(response.Body.Bytes(), &blocker) != nil || blocker.Code != "adoption_source_class_refused" {
		t.Fatalf("legacy_managed admission: %d %s", response.Code, response.Body.String())
	}
	after, err := app.store.Load(t.Context())
	if err != nil || after.Operation.Revision != before.Operation.Revision || after.Operation.Strategy.Strategy != "" || after.AdoptionCiphertext != "" {
		t.Fatalf("refused legacy_managed admission mutated Fresh authority: %+v %v", after.Operation.Strategy, err)
	}
}
