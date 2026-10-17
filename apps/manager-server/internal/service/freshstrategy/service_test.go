package freshstrategy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

func operation() bootstrap.FreshOperation {
	return bootstrap.FreshOperation{Version: 1, Identity: bootstrap.FreshIdentity{AdmissionID: "admission", InstallationID: "installation", Generation: "generation", DatabasePath: "/private/manager.sqlite", OperationID: "fresh-operation-123"}, Revision: 1, AdminInitialized: true, GatewayPending: true}
}

func qualified() bootstrap.CPAQualification {
	return bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true}
}

type managedRuntime struct {
	runtimeservice.RuntimeClient
	status model.RuntimeObservedStatus
	starts []model.RuntimeMutationRequest
}

func (r *managedRuntime) Status(context.Context) (model.RuntimeObservedStatus, error) {
	return r.status, nil
}
func (r *managedRuntime) Start(_ context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	r.starts = append(r.starts, request)
	return model.RuntimeOperationResult{OperationID: request.OperationID, OperationType: model.RuntimeOperationStart, RuntimeIdentity: r.status.Identity, RuntimeGeneration: r.status.Generation, State: model.RuntimeOperationSucceeded}, nil
}

func TestManagedRequiresDurableExactIntentAndReusesTypedRuntime(t *testing.T) {
	artifact := model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64))
	runtime := &managedRuntime{status: model.RuntimeObservedStatus{Identity: "target-one", Generation: 3, ProtocolVersion: "v1", State: model.RuntimeStateOffline, Capabilities: model.RuntimeCapabilities{model.RuntimeCapabilityStart}, ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: artifact}}}
	request := Request{Strategy: bootstrap.NewManaged, ExpectedRuntimeIdentity: "target-one", ExpectedRuntimeGeneration: 3, ExpectedArtifactID: artifact}
	service := New(Options{Runtime: runtime, Qualification: qualified(), QualifiedArtifactID: artifact})
	op := operation()
	if _, err := New(Options{Runtime: runtime, Qualification: qualified(), QualifiedArtifactID: model.RuntimeArtifactID("sha256:" + strings.Repeat("b", 64))}).Execute(context.Background(), op, request); err == nil || len(runtime.starts) != 0 {
		t.Fatal("generic qualification promoted a different exact artifact")
	}
	staged, err := service.Execute(context.Background(), op, request)
	if err != nil || staged.Checkpoint.Stage != "staged" || len(runtime.starts) != 0 || !staged.GatewayPending {
		t.Fatalf("stage: %+v %v", staged, err)
	}
	op.Strategy = staged.Checkpoint // simulates the caller's durable CAS checkpoint
	committed, err := New(Options{Runtime: runtime, Qualification: qualified(), QualifiedArtifactID: artifact}).Execute(context.Background(), op, request)
	if err != nil || committed.Checkpoint.Stage != "committed" || len(runtime.starts) != 1 || runtime.starts[0].ExpectedRuntimeGeneration != 3 || runtime.starts[0].ExpectedActiveArtifactID != artifact || runtime.starts[0].OperationID != "fresh-operation-123:managed-start" || !committed.GatewayPending {
		t.Fatalf("start: %+v %v", committed, err)
	}
	// Lost response retries the exact Supervisor operation; no second owner.
	_, err = service.Execute(context.Background(), op, request)
	if err != nil || len(runtime.starts) != 2 || runtime.starts[0] != runtime.starts[1] {
		t.Fatal("retry changed durable operation ownership")
	}
	op.Strategy = committed.Checkpoint
	_, err = service.Execute(context.Background(), op, request)
	if err != nil || len(runtime.starts) != 2 {
		t.Fatal("committed retry restarted target")
	}
	runtime.status.Generation++
	if _, err = service.Execute(context.Background(), op, request); err == nil {
		t.Fatal("accepted foreign runtime generation")
	}
}

func TestManagedUnqualifiedOrForeignTargetCannotStart(t *testing.T) {
	service := New(Options{})
	if _, err := service.Execute(context.Background(), operation(), Request{Strategy: bootstrap.NewManaged}); err == nil {
		t.Fatal("unqualified artifact accepted")
	}
	op := operation()
	op.Strategy.Strategy = bootstrap.ConnectExternal
	if _, err := service.Execute(context.Background(), op, Request{Strategy: bootstrap.NewManaged}); err == nil {
		t.Fatal("foreign strategy owner accepted")
	}
	op = operation()
	op.AdminInitialized = false
	if _, err := service.Execute(context.Background(), op, Request{Strategy: bootstrap.NewManaged}); err == nil {
		t.Fatal("uninitialized admin accepted")
	}
}

func TestManagedReplayRejectsChangedRequestForEveryDurableStage(t *testing.T) {
	artifact := model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64))
	runtime := &managedRuntime{status: model.RuntimeObservedStatus{Identity: "target-one", Generation: 3, ProtocolVersion: "v1", State: model.RuntimeStateReady, Capabilities: model.RuntimeCapabilities{model.RuntimeCapabilityStart}, ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: artifact}}}
	service := New(Options{Runtime: runtime, Qualification: qualified(), QualifiedArtifactID: artifact})
	for _, stage := range []string{"staged", "starting", "committed"} {
		op := operation()
		op.Strategy = bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, Stage: stage, TargetIdentity: "target-one", TargetGeneration: 3, TargetArtifactID: string(artifact)}
		for _, request := range []Request{
			{Strategy: bootstrap.NewManaged},
			{Strategy: bootstrap.NewManaged, ExpectedRuntimeIdentity: "foreign", ExpectedRuntimeGeneration: 3, ExpectedArtifactID: artifact},
			{Strategy: bootstrap.NewManaged, ExpectedRuntimeIdentity: "target-one", ExpectedRuntimeGeneration: 4, ExpectedArtifactID: artifact},
			{Strategy: bootstrap.NewManaged, ExpectedRuntimeIdentity: "target-one", ExpectedRuntimeGeneration: 3, ExpectedArtifactID: model.RuntimeArtifactID("sha256:" + strings.Repeat("b", 64))},
		} {
			_, err := service.Execute(t.Context(), op, request)
			var blocker *Blocker
			if !errors.As(err, &blocker) || blocker.Code != "managed_target_identity_mismatch" || len(runtime.starts) != 0 {
				t.Fatalf("%s replay bypassed exact request identity: %v", stage, err)
			}
		}
	}
}

func TestExternalStoresOnlyEncryptedManagementSecretAndKeepsGatewayPending(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v8/management/config" {
			t.Errorf("unsafe source access")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
	}))
	defer server.Close()
	protector, _ := security.NewProtector([]byte("task-specific-data-key"))
	service := New(Options{Protector: protector, Qualification: qualified()})
	request := Request{Strategy: bootstrap.ConnectExternal, BaseURL: server.URL, ManagementKey: "private-management-secret"}
	result, err := service.Execute(context.Background(), operation(), request)
	if err != nil || result.Checkpoint.Stage != "committed" || !result.GatewayPending || result.RuntimeMode != model.RuntimeModeExternal || !security.IsProtected(result.Connection.ManagementKeyCiphertext) {
		t.Fatalf("external: %+v %v", result, err)
	}
	secret, err := protector.UnprotectString(result.Connection.ManagementKeyCiphertext)
	if err != nil || secret != request.ManagementKey {
		t.Fatal("encrypted handoff invalid")
	}
	public, _ := json.Marshal(result)
	if strings.Contains(string(public), "private-management") || strings.Contains(string(public), "enc:v1:") {
		t.Fatal("connection leaked to public result")
	}
	op := operation()
	op.Strategy = result.Checkpoint
	retry, err := service.Execute(context.Background(), op, request)
	if err != nil || retry.Checkpoint.TargetIdentity != result.Checkpoint.TargetIdentity || calls.Load() != 2 {
		t.Fatal("external retry changed target")
	}
	if _, err := New(Options{Protector: protector}).Execute(context.Background(), operation(), request); err == nil {
		t.Fatal("safe read elevated to qualified profile")
	}
	if calls.Load() != 2 {
		t.Fatal("unqualified External touched source")
	}
}

func TestAdoptionPreservesSourceAndExplicitlyBlocksUnsupportedReplay(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v8/management/config" {
			t.Errorf("adoption touched source lifecycle/history/queue")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]},"future-resource":{"secret":"never-return"}}`))
	}))
	defer server.Close()
	result, err := New(Options{Qualification: qualified()}).Execute(context.Background(), operation(), Request{Strategy: bootstrap.AdoptExisting, BaseURL: server.URL, ManagementKey: "private", AdoptSourceClass: bootstrap.AdoptNetworkExisting})
	var blocker *Blocker
	if !errors.As(err, &blocker) || blocker.Code != "adoption_destination_staging_required" || !result.SourcePreserved || result.Checkpoint.Stage == "committed" || calls.Load() != 0 {
		t.Fatalf("adoption: %+v %v", result, err)
	}
	if !strings.Contains(strings.Join(blocker.Unsupported, ","), "cpa_native_destination_staging_required") {
		t.Fatal("adoption blocker lost the accepted staging prerequisite")
	}
	if _, err := New(Options{}).Execute(context.Background(), operation(), Request{Strategy: bootstrap.AdoptExisting, BaseURL: server.URL, ManagementKey: "private", AdoptSourceClass: bootstrap.AdoptNetworkExisting}); err == nil || calls.Load() != 0 {
		t.Fatal("unqualified adoption touched source")
	}
	public, _ := json.Marshal(result)
	if strings.Contains(string(public), "never-return") {
		t.Fatal("source secrets escaped")
	}
	// Source remains usable and receives no stop/delete/replace or usage Pop.
	response, err := http.Get(server.URL + "/v8/management/config")
	if err != nil || response.StatusCode != 200 {
		t.Fatal("source unavailable after failed adoption")
	}
	_ = response.Body.Close()
}
