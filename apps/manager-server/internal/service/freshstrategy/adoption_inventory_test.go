package freshstrategy

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func TestAdoptionSourceClassUsesOnlyItsExplicitReadSurface(t *testing.T) {
	for _, class := range []bootstrap.AdoptSourceClass{bootstrap.AdoptNetworkExisting, bootstrap.AdoptLegacyManaged} {
		t.Run(string(class), func(t *testing.T) {
			var v8Calls, v0Calls atomic.Int32
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v8/management/config":
					v8Calls.Add(1)
					_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"source-auth"},"access":{"api-keys":["source-client"]},"providers":{}}`))
				case "/v0/management/config":
					v0Calls.Add(1)
					w.Header().Set("X-CPA-VERSION", "7.0.12")
					_, _ = w.Write([]byte(`{"tls":{"enable":false},"routing":{"strategy":"round-robin"},"gemini-api-key":[{"api-key":"private-provider-secret"}],"api-keys":["source-client"]}`))
				default:
					t.Errorf("unexpected source access: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer source.Close()
			artifact := model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64))
			runtime := &adoptionRuntime{managedRuntime: managedRuntime{status: model.RuntimeObservedStatus{Identity: "destination-runtime", Generation: 3, ProtocolVersion: "v1", State: model.RuntimeStateReady, Capabilities: model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStageDestination}, ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: artifact}}}}
			target := httptest.NewServer(http.HandlerFunc(runtime.target))
			defer target.Close()
			protector, _ := security.NewProtector([]byte("test-only-source-routing-key"))
			q := qualified()
			q.StagingV8TransitionQualified = class == bootstrap.AdoptLegacyManaged
			options := Options{Runtime: runtime, Protector: protector, Qualification: q, QualifiedArtifactID: artifact, AdoptionResources: acceptedKeys(), DestinationBaseURL: target.URL}
			request := Request{Strategy: bootstrap.AdoptExisting, BaseURL: source.URL, ManagementKey: "source-management", ExpectedRuntimeIdentity: runtime.status.Identity, ExpectedRuntimeGeneration: 3, ExpectedArtifactID: artifact, AdoptSourceClass: class}
			if _, err := New(options).Execute(t.Context(), operation(), request); err != nil {
				t.Fatalf("staged %s: %v", class, err)
			}
			if class == bootstrap.AdoptNetworkExisting {
				if v8Calls.Load() != 1 || v0Calls.Load() != 0 {
					t.Fatalf("network adoption used a fallback read: v8=%d v0=%d", v8Calls.Load(), v0Calls.Load())
				}
			} else if v0Calls.Load() != 1 || v8Calls.Load() != 0 {
				t.Fatalf("legacy adoption used a v8 read or fallback: v8=%d v0=%d", v8Calls.Load(), v0Calls.Load())
			}
			// Unqualified or missing-destination requests must not touch either
			// surface, including after a staged checkpoint exists.
			before := v8Calls.Load() + v0Calls.Load()
			op := operation()
			for _, blocked := range []Options{
				{Runtime: runtime, Protector: protector, QualifiedArtifactID: artifact, AdoptionResources: acceptedKeys(), DestinationBaseURL: target.URL},
				{Runtime: runtime, Protector: protector, Qualification: q, QualifiedArtifactID: artifact, AdoptionResources: acceptedKeys()},
			} {
				if _, err := New(blocked).Execute(t.Context(), op, request); err == nil {
					t.Fatal("blocked adopt path succeeded")
				}
			}
			if v8Calls.Load()+v0Calls.Load() != before {
				t.Fatal("blocked adoption path touched the source")
			}
		})
	}
}

func TestAdoptionInventoryMappingIsBounded(t *testing.T) {
	refs, err := adoptionResourceRefs([]string{"access", "oauth", "unknown_cpa_native_resource", "providers"})
	want := []bootstrap.ResourceRef{bootstrap.AdoptionRefClientAPIKeys, bootstrap.AdoptionRefCPAAuthFiles, bootstrap.AdoptionRefConfigUnknown, bootstrap.AdoptionRefProviderStateProviders}
	if err != nil || !reflect.DeepEqual(refs, want) {
		t.Fatalf("bounded mapping: %v %v", refs, err)
	}
	for name, sections := range map[string][]string{
		"unknown section":        {"access", "oauth", "attacker-controlled-key"},
		"duplicate section":      {"access", "oauth", "access"},
		"missing client keys":    {"oauth", "server"},
		"missing auth files":     {"access", "server"},
		"no inventory":           nil,
		"provider only":          {"providers"},
		"unknown runtime string": {"access", "oauth", "server", "ACCESS"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := adoptionResourceRefs(sections); err == nil {
				t.Fatal("unbounded discovered inventory accepted")
			}
		})
	}
	// Only the accepted client-key decision may migrate; every mapped manual
	// resource keeps its explicit disposition.
	decisions, ok := adoptionDecisionsForInventory(want, acceptedKeys()[0])
	if !ok {
		t.Fatal("bounded inventory produced no decisions")
	}
	for i, decision := range decisions {
		if decision.ResourceRef != want[i] {
			t.Fatal("decision order does not follow the durable inventory")
		}
		if decision.ResourceRef == bootstrap.AdoptionRefClientAPIKeys {
			if decision.Disposition != bootstrap.Migrate {
				t.Fatal("client keys did not migrate")
			}
			continue
		}
		if decision.Disposition != bootstrap.ManualAction || decision.ManualAction == "" || decision.Qualification != bootstrap.CapabilityPending {
			t.Fatalf("non-client resource migrated or dropped: %+v", decision)
		}
	}
	if _, ok := adoptionDecisionsForInventory([]bootstrap.ResourceRef{bootstrap.AdoptionRefCPAAuthFiles}, acceptedKeys()[0]); ok {
		t.Fatal("inventory without client keys produced decisions")
	}
	// An upstream-controlled reference can never be turned into a decision.
	if _, ok := adoptionDecisionsForInventory([]bootstrap.ResourceRef{bootstrap.AdoptionRefClientAPIKeys, "attacker-controlled-key"}, acceptedKeys()[0]); ok {
		t.Fatal("unbounded resource reference accepted")
	}
}

func TestAdoptionRejectsUnacceptedLegacySourceProfileWithoutEffects(t *testing.T) {
	for _, version := range []string{"", "7.0.11", "7.0.13", "8.0.13", "garbage"} {
		t.Run("version="+version, func(t *testing.T) {
			var calls atomic.Int32
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if version != "" {
					w.Header().Set("X-CPA-VERSION", version)
				}
				_, _ = w.Write([]byte(`{"tls":{"enable":false},"api-keys":["source-client"]}`))
			}))
			defer source.Close()
			artifact := model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64))
			runtime := &adoptionRuntime{managedRuntime: managedRuntime{status: model.RuntimeObservedStatus{Identity: "destination-runtime", Generation: 3, ProtocolVersion: "v1", State: model.RuntimeStateReady, Capabilities: model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStageDestination}, ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: artifact}}}}
			target := httptest.NewServer(http.HandlerFunc(runtime.target))
			defer target.Close()
			protector, _ := security.NewProtector([]byte("test-only-legacy-profile-key"))
			q := qualified()
			q.StagingV8TransitionQualified = true
			options := Options{Runtime: runtime, Protector: protector, Qualification: q, QualifiedArtifactID: artifact, AdoptionResources: acceptedKeys(), DestinationBaseURL: target.URL}
			result, err := New(options).Execute(t.Context(), operation(), Request{Strategy: bootstrap.AdoptExisting, BaseURL: source.URL, ManagementKey: "source-management", ExpectedRuntimeIdentity: runtime.status.Identity, ExpectedRuntimeGeneration: 3, ExpectedArtifactID: artifact, AdoptSourceClass: bootstrap.AdoptLegacyManaged})
			var blocker *Blocker
			if !errors.As(err, &blocker) || blocker.Code != "adoption_source_read_unavailable" || result.Checkpoint.Strategy != "" || result.AdoptionCiphertext != "" ||
				len(runtime.stages) != 0 || len(runtime.starts) != 0 || calls.Load() != 1 {
				t.Fatalf("unaccepted legacy source profile had effects: %+v %v calls=%d", result, err, calls.Load())
			}
		})
	}
	// Positive control: the exact accepted profile still stages without
	// destination mutation.
	var calls atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-CPA-VERSION", "7.0.12")
		_, _ = w.Write([]byte(`{"tls":{"enable":false},"api-keys":["source-client"]}`))
	}))
	defer source.Close()
	artifact := model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64))
	runtime := &adoptionRuntime{managedRuntime: managedRuntime{status: model.RuntimeObservedStatus{Identity: "destination-runtime", Generation: 3, ProtocolVersion: "v1", State: model.RuntimeStateReady, Capabilities: model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStageDestination}, ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: artifact}}}}
	target := httptest.NewServer(http.HandlerFunc(runtime.target))
	defer target.Close()
	protector, _ := security.NewProtector([]byte("test-only-legacy-profile-key"))
	q := qualified()
	q.StagingV8TransitionQualified = true
	options := Options{Runtime: runtime, Protector: protector, Qualification: q, QualifiedArtifactID: artifact, AdoptionResources: acceptedKeys(), DestinationBaseURL: target.URL}
	staged, err := New(options).Execute(t.Context(), operation(), Request{Strategy: bootstrap.AdoptExisting, BaseURL: source.URL, ManagementKey: "source-management", ExpectedRuntimeIdentity: runtime.status.Identity, ExpectedRuntimeGeneration: 3, ExpectedArtifactID: artifact, AdoptSourceClass: bootstrap.AdoptLegacyManaged})
	if err != nil || staged.Checkpoint.Stage != "staged" || len(runtime.stages) != 0 || len(runtime.starts) != 0 || calls.Load() != 1 {
		t.Fatalf("accepted legacy profile did not stage: %v", err)
	}
}
