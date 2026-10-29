package freshstrategy

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

// adoptionPluginFixture is one staged adoption over a source that keeps its own
// plugin posture: the destination is this Supervisor's CPA, so the operator's
// opt-in belongs to it.
func adoptionPluginFixture(t *testing.T) (*Service, *adoptionRuntime, Request) {
	t.Helper()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v8/management/config" || r.Header.Get("Authorization") != "Bearer source-management" {
			t.Error("source lifecycle/history/config mutated")
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// The source's own plugins stay as they are: only access, server and the
		// auth directory are read where the item universe names them.
		_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"source-auth"},"access":{"api-keys":["source-client-one","source-client-two"]},"plugins":{"enabled":true}}`))
	}))
	t.Cleanup(source.Close)
	artifact := model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64))
	runtime := &adoptionRuntime{managedRuntime: managedRuntime{status: model.RuntimeObservedStatus{Identity: "destination-runtime", Generation: 3, ProtocolVersion: "v1", State: model.RuntimeStateReady, Capabilities: model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStageDestination}, ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: artifact}}}}
	target := httptest.NewServer(http.HandlerFunc(runtime.target))
	t.Cleanup(target.Close)
	protector, _ := security.NewProtector([]byte("test-only-adoption-data-key"))
	service := New(Options{Runtime: runtime, Protector: protector, Qualification: qualified(), QualifiedArtifactID: artifact, AdoptionResources: acceptedKeys(), DestinationBaseURL: target.URL})
	return service, runtime, Request{Strategy: bootstrap.AdoptExisting, BaseURL: source.URL, ManagementKey: "source-management", ExpectedRuntimeIdentity: runtime.status.Identity, ExpectedRuntimeGeneration: 3, ExpectedArtifactID: artifact, AdoptSourceClass: bootstrap.AdoptNetworkExisting}
}

// TestAdoptionPersistsThePluginOptInIntoTheDestinationTemplate proves the staged
// request carries the operator's decision: the destination template writes
// exactly `plugins.enabled` from it.
func TestAdoptionPersistsThePluginOptInIntoTheDestinationTemplate(t *testing.T) {
	service, runtime, request := adoptionPluginFixture(t)
	request.PluginsEnabled = true
	staged, err := service.Execute(t.Context(), operation(), request)
	if err != nil || staged.Checkpoint.Stage != "staged" {
		t.Fatalf("stage: %v", err)
	}
	if !staged.Checkpoint.PluginsEnabled {
		t.Fatal("checkpoint did not persist the opt-in")
	}
	protector, _ := security.NewProtector([]byte("test-only-adoption-data-key"))
	_ = protector
	// The destination has not been staged yet, so the request is still only in the
	// protected snapshot; the replay below stages it and the template is read back.
	op := operation()
	op.Strategy = staged.Checkpoint
	request.AdoptionCiphertext = staged.AdoptionCiphertext
	committed, err := service.Execute(t.Context(), op, request)
	if err != nil || committed.Checkpoint.Stage != "committed" {
		t.Fatalf("commit: %v", err)
	}
	if len(runtime.stages) != 1 || !runtime.stages[0].PluginsEnabled {
		t.Fatal("destination template did not receive the plugin opt-in")
	}
	if !committed.Checkpoint.PluginsEnabled {
		t.Fatal("committed checkpoint lost the opt-in")
	}
}

// TestAdoptionReplayRejectsAChangedPluginOptIn keeps the recorded decision
// immutable: a retry presenting the other value claims a decision this operation
// never made.
func TestAdoptionReplayRejectsAChangedPluginOptIn(t *testing.T) {
	service, runtime, request := adoptionPluginFixture(t)
	staged, err := service.Execute(t.Context(), operation(), request)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	op := operation()
	op.Strategy = staged.Checkpoint
	request.AdoptionCiphertext = staged.AdoptionCiphertext
	request.PluginsEnabled = true
	_, err = service.Execute(t.Context(), op, request)
	var blocker *Blocker
	if !errors.As(err, &blocker) || blocker.Code != "adopt_plugin_optin_conflict" || len(runtime.stages) != 0 {
		t.Fatalf("replay accepted a changed opt-in: %v", err)
	}
}

// TestExternalStillRefusesAPluginOptIn keeps the one strategy that owns no
// destination in this Supervisor out of the opt-in.
func TestExternalStillRefusesAPluginOptIn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("source contacted for a refused opt-in")
		w.WriteHeader(500)
	}))
	defer server.Close()
	protector, _ := security.NewProtector([]byte("task-specific-data-key"))
	service := New(Options{Protector: protector, Qualification: qualified()})
	_, err := service.Execute(t.Context(), operation(), Request{Strategy: bootstrap.ConnectExternal, BaseURL: server.URL, ManagementKey: "private-management-secret", PluginsEnabled: true})
	var blocker *Blocker
	if !errors.As(err, &blocker) || blocker.Code != "fresh_strategy_plugin_scope_conflict" {
		t.Fatalf("external opt-in permitted: %v", err)
	}
}
