package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

type installationTestRuntime struct {
	// Any unexpected Runtime mutation panics through the nil interface.
	runtimeservice.RuntimeClient
	status model.RuntimeObservedStatus
	err    error
	calls  int
}

func (r *installationTestRuntime) Status(ctx context.Context) (model.RuntimeObservedStatus, error) {
	r.calls++
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
		return model.RuntimeObservedStatus{}, errors.New("Runtime observation must have a bounded timeout")
	}
	return r.status, r.err
}

func validInstallationRuntime() model.RuntimeObservedStatus {
	return model.RuntimeObservedStatus{
		Identity: "installation-runtime", Generation: 7, ProtocolVersion: "v1", State: model.RuntimeStateReady,
		ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64))},
	}
}

func newInstallationTestApplication(t *testing.T) *freshApplication {
	t.Helper()
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	app := newFreshApplication(opened, config.Config{}, nil)
	_, err = app.store.Update(t.Context(), opened.Record.Operation.Identity, opened.Record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: r.Operation.Identity.OperationID, Revision: 1, Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func installationAPI(t *testing.T, app *freshApplication, method, resource, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/control/api/v2/"+resource, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	app.canonicalAPI(response, req)
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("installation API did not prevent response caching")
	}
	return response
}

func installManagedCheckpoint(t *testing.T, app *freshApplication, stage string) bootstrap.FreshOperation {
	t.Helper()
	before, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	s := validInstallationRuntime()
	after, err := app.store.Update(t.Context(), before.Operation.Identity, before.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.NewManaged, Stage: stage, TargetIdentity: string(s.Identity),
			TargetGeneration: uint64(s.Generation), TargetArtifactID: string(s.ActiveGatewayArtifact.ArtifactID),
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return after.Operation
}

func TestFreshInstallationAPIRequiresAdminBeforeRuntimeObservation(t *testing.T) {
	app := newInstallationTestApplication(t)
	runtime := &installationTestRuntime{status: validInstallationRuntime()}
	app.runtime = runtime
	for _, token := range []string{"", "invalid-admin-key"} {
		for _, request := range []struct{ method, resource string }{{http.MethodGet, "installation/runtime"}, {http.MethodPost, "complete"}} {
			response := installationAPI(t, app, request.method, request.resource, token)
			if response.Code != http.StatusUnauthorized || response.Body.Len() != 0 || runtime.calls != 0 {
				t.Fatalf("unauthenticated request accessed installation state: %d %s", response.Code, response.Body.String())
			}
		}
	}
}

func TestFreshInstallationRuntimeProjectsOnlyReadOnlyTarget(t *testing.T) {
	app := newInstallationTestApplication(t)
	s := validInstallationRuntime()
	s.ActiveGatewayArtifact.Version = "/private/runtime/token-location"
	s.CPAObservedVersion = model.CPAObservedVersion(s.ActiveGatewayArtifact.Version)
	s.Capabilities = model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStageDestination}
	s.Recovery = &model.RuntimeRecoveryObservation{State: model.RuntimeRecoveryStateManualIntervention, AttemptsRemaining: 0}
	runtime := &installationTestRuntime{status: s}
	app.runtime = runtime
	before, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	response := installationAPI(t, app, http.MethodGet, "installation/runtime", strategyTestAdmin)
	if response.Code != http.StatusOK || runtime.calls != 1 {
		t.Fatalf("observation failed: %d %s", response.Code, response.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"runtimeIdentity": string(s.Identity), "runtimeGeneration": float64(s.Generation), "state": string(s.State), "activeGatewayArtifact": map[string]any{"artifactId": string(s.ActiveGatewayArtifact.ArtifactID)}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Runtime projection leaked or changed fields: %#v", got)
	}
	after, err := app.store.Load(t.Context())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("read-only Runtime observation changed persisted installation", err)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		if response := installationAPI(t, app, method, "installation/runtime", strategyTestAdmin); response.Code == http.StatusOK || runtime.calls != 1 {
			t.Fatal("non-GET Runtime observation allowed mutation")
		}
	}
}

func TestFreshInstallationRuntimeUnavailableOrInvalidFailsClosed(t *testing.T) {
	for _, kind := range []string{"missing", "transport-error", "identity", "generation", "protocol", "state", "artifact-missing", "artifact-invalid"} {
		t.Run(kind, func(t *testing.T) {
			app := newInstallationTestApplication(t)
			runtime := &installationTestRuntime{status: validInstallationRuntime()}
			app.runtime = runtime
			switch kind {
			case "missing":
				app.runtime = nil
			case "transport-error":
				runtime.err = errors.New("token=secret /private/runtime/socket")
			case "identity":
				runtime.status.Identity = ""
			case "generation":
				runtime.status.Generation = 0
			case "protocol":
				runtime.status.ProtocolVersion = "v2"
			case "state":
				runtime.status.State = "unrecognized"
			case "artifact-missing":
				runtime.status.ActiveGatewayArtifact = nil
			case "artifact-invalid":
				runtime.status.ActiveGatewayArtifact.ArtifactID = "invalid"
			}
			response := installationAPI(t, app, http.MethodGet, "installation/runtime", strategyTestAdmin)
			if response.Code != http.StatusServiceUnavailable || response.Body.Len() != 0 {
				t.Fatalf("invalid observation was exposed: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestFreshInstallationRuntimeRejectsOtherStrategiesWithoutCallingRuntime(t *testing.T) {
	app := newInstallationTestApplication(t)
	runtime := &installationTestRuntime{status: validInstallationRuntime()}
	app.runtime = runtime
	// Connect External owns no Manager Runtime: its CPA is the user's, so the
	// observation route refuses it before any status read.
	response := httptest.NewRecorder()
	app.installationRuntime(response, httptest.NewRequest(http.MethodGet, "/control/api/v2/installation/runtime", nil), bootstrap.FreshOperation{Strategy: bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.ConnectExternal}})
	if response.Code != http.StatusConflict || runtime.calls != 0 {
		t.Fatal("External acquired managed Runtime observation")
	}
	// Adopt is a Manager-owned installation: the destination the Manager staged
	// is its own Runtime, so the same observation answers for it.
	adopted := httptest.NewRecorder()
	app.installationRuntime(adopted, httptest.NewRequest(http.MethodGet, "/control/api/v2/installation/runtime", nil), bootstrap.FreshOperation{Strategy: bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.AdoptExisting, SourceClass: bootstrap.AdoptNetworkExisting}})
	if adopted.Code != http.StatusOK || runtime.calls != 1 {
		t.Fatalf("adopted installation lost its Runtime observation: %d", adopted.Code)
	}
}

func TestFreshCompleteRequiresCommittedMatchingReadyRuntime(t *testing.T) {
	for _, kind := range []string{"unselected", "staged", "starting-checkpoint", "missing-runtime", "transport-error", "foreign-identity", "foreign-generation", "foreign-artifact", "offline", "starting-runtime", "invalid-runtime", "wrong-protocol"} {
		t.Run(kind, func(t *testing.T) {
			app := newInstallationTestApplication(t)
			if kind != "unselected" {
				stage := "committed"
				if kind == "staged" {
					stage = "staged"
				}
				if kind == "starting-checkpoint" {
					stage = "starting"
				}
				installManagedCheckpoint(t, app, stage)
			}
			runtime := &installationTestRuntime{status: validInstallationRuntime()}
			app.runtime = runtime
			switch kind {
			case "missing-runtime":
				app.runtime = nil
			case "transport-error":
				runtime.err = errors.New("private Runtime error")
			case "foreign-identity":
				runtime.status.Identity = "different-runtime"
			case "foreign-generation":
				runtime.status.Generation++
			case "foreign-artifact":
				runtime.status.ActiveGatewayArtifact.ArtifactID = model.RuntimeArtifactID("sha256:" + strings.Repeat("b", 64))
			case "offline":
				runtime.status.State = model.RuntimeStateOffline
			case "starting-runtime":
				runtime.status.State = model.RuntimeStateStarting
			case "invalid-runtime":
				runtime.status.Generation = 0
			case "wrong-protocol":
				runtime.status.ProtocolVersion = "v2"
			}
			before, err := app.store.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			response := installationAPI(t, app, http.MethodPost, "complete", strategyTestAdmin)
			if response.Code != http.StatusServiceUnavailable || response.Body.Len() != 0 {
				t.Fatalf("unproven Runtime completed installation: %d %s", response.Code, response.Body.String())
			}
			after, err := app.store.Load(t.Context())
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("rejected completion changed persisted installation", err)
			}
		})
	}
}

func TestFreshCompletePersistsAndReplaysWithoutDependingOnRuntime(t *testing.T) {
	app := newInstallationTestApplication(t)
	initial := installManagedCheckpoint(t, app, "committed")
	runtime := &installationTestRuntime{status: validInstallationRuntime()}
	app.runtime = runtime
	response := installationAPI(t, app, http.MethodPost, "complete", strategyTestAdmin)
	if response.Code != http.StatusOK || runtime.calls != 1 {
		t.Fatalf("ready installation could not complete: %d %s", response.Code, response.Body.String())
	}
	completed, err := app.store.Load(t.Context())
	if err != nil || !completed.Operation.ProductComplete || completed.Operation.Identity != initial.Identity || completed.Operation.Revision != initial.Revision+1 {
		t.Fatal("completion did not preserve operation identity and advance its checkpoint", err)
	}
	app.runtime = nil // Runtime outage after a lost completion response.
	for replay := 0; replay < 2; replay++ {
		response := installationAPI(t, app, http.MethodPost, "complete", strategyTestAdmin)
		if response.Code != http.StatusOK {
			t.Fatalf("lost completion response could not reconcile: %d", response.Code)
		}
		after, err := app.store.Load(t.Context())
		if err != nil || !reflect.DeepEqual(completed, after) || runtime.calls != 1 {
			t.Fatal("completion replay changed persisted state or contacted Runtime", err)
		}
	}
}
