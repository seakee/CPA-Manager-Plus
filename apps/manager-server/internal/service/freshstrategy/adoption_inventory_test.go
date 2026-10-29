package freshstrategy

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
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
	// A root is not one resource: the record holds a decision for every reference
	// an item under the reported roots can carry. `access` adds the client keys
	// and the unknown configuration a path under it would be, `oauth` adds the
	// auth directory and its copyable provider maps, `providers` its own section
	// reference, and the unknown root the gateway configuration the reader does
	// not name.
	refs, err := adoptionResourceRefs([]string{"access", "oauth", "unknown_cpa_native_resource", "providers"})
	want := []bootstrap.ResourceRef{
		bootstrap.AdoptionRefClientAPIKeys, bootstrap.AdoptionRefConfigUnknown,
		bootstrap.AdoptionRefCPAAuthFiles, bootstrap.AdoptionRefProviderStateAPIKeys,
		bootstrap.AdoptionRefConfigRouting, bootstrap.AdoptionRefProviderStateProviders,
	}
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
	// Exactly the accepted evidence's kinds that this source carries migrate;
	// every mapped manual resource keeps its explicit disposition.
	decisions, ok := adoptionDecisionsForInventory(want, acceptedKeyDecisionMap())
	if !ok {
		t.Fatal("bounded inventory produced no decisions")
	}
	migrated := acceptedKeyDecisionMap()
	for i, decision := range decisions {
		if decision.ResourceRef != want[i] {
			t.Fatal("decision order does not follow the durable inventory")
		}
		if _, ok := migrated[decision.ResourceRef]; ok {
			if decision.Disposition != bootstrap.Migrate || decision.Qualification != bootstrap.CapabilityQualified {
				t.Fatalf("authorized resource did not migrate: %+v", decision)
			}
			continue
		}
		if decision.Disposition != bootstrap.ManualAction || decision.ManualAction == "" || decision.Qualification != bootstrap.CapabilityPending {
			t.Fatalf("unauthorized resource migrated or dropped: %+v", decision)
		}
	}
	if _, ok := adoptionDecisionsForInventory([]bootstrap.ResourceRef{bootstrap.AdoptionRefCPAAuthFiles}, acceptedKeyDecisionMap()); ok {
		t.Fatal("inventory without client keys produced decisions")
	}
	// An upstream-controlled reference can never be turned into a decision.
	if _, ok := adoptionDecisionsForInventory([]bootstrap.ResourceRef{bootstrap.AdoptionRefClientAPIKeys, "attacker-controlled-key"}, acceptedKeyDecisionMap()); ok {
		t.Fatal("unbounded resource reference accepted")
	}
}

// The record must cover the executor. Discovery records a decision per reference
// a reported root contributes; the executor gates each item on its own reference.
// Over the roots the controlled real-CPA source reports, every item the executor
// can enumerate under one of them must therefore have a recorded decision — the
// drift this asserts against is a root recorded as manual while its items are
// copied.
func TestAdoptionRecordCoversEveryEnumeratedItemUnderTheReportedRoots(t *testing.T) {
	// The v8 fixture of `adoption_cpa_test.go` reports these roots: its config
	// document carries `server`, `management`, `oauth`, `access`, `plugins`,
	// `routing`, `client` and `api-keys`, and `requests`/`multimedia`/`upstream`
	// arrive under the reader's unknown catch-all.
	roots := []string{"server", "management", "access", "oauth", "api-keys", "routing", "client", "plugins", "unknown_cpa_native_resource"}
	refs, err := adoptionResourceRefs(roots)
	if err != nil {
		t.Fatalf("the reported roots are not a bounded inventory: %v", err)
	}
	recorded := map[bootstrap.ResourceRef]bool{}
	for _, ref := range refs {
		recorded[ref] = true
	}
	// Per root, not merely over the union: an item must be covered by the
	// reference the decision list holds *for its own root*. A union check would
	// pass while one root's set was wrong, because another root happened to
	// contribute the same reference — exactly how the drift stayed hidden.
	for _, item := range cpa.AdoptionItemUniverse() {
		root := cpa.AdoptionRootOf(item)
		if !slices.Contains(roots, root) {
			continue
		}
		ref := cpa.AdoptionItemRef(item)
		if !slices.Contains(cpa.AdoptionRootRefs(root), ref) {
			t.Fatalf("item %s is gated under %s but root %q contributes %v", item, ref, root, cpa.AdoptionRootRefs(root))
		}
		if !recorded[ref] {
			t.Fatalf("item %s is gated under %s, which the record never decided: %v", item, ref, refs)
		}
	}
	// The gateway configuration under `requests`, `multimedia` and `upstream` is
	// the case that used to drift: it classifies as routing configuration while
	// the root itself contributes the unknown reference.
	if !recorded[bootstrap.AdoptionRefConfigRouting] || !recorded[bootstrap.AdoptionRefConfigUnknown] {
		t.Fatalf("the unknown root did not contribute both of its references: %v", refs)
	}
	for _, item := range []string{"requests.proxy-url", "multimedia.disable-image-generation", "upstream.codex.response-steering"} {
		ref := cpa.AdoptionItemRef(item)
		if ref != bootstrap.AdoptionRefConfigRouting {
			t.Fatalf("%s classifies as %s", item, ref)
		}
		if !recorded[ref] {
			t.Fatalf("%s is gated under an undecided reference", item)
		}
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
