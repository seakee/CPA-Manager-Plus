package freshstrategy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

// acceptedKeys is the resource decision set the accepted evidence authorizes: one
// decision per migrated reference, exactly as the production loader derives them.
func acceptedKeys() []bootstrap.AdoptionResourceDecision {
	return decisionsForMigratedRefs()
}

func decisionsForMigratedRefs() []bootstrap.AdoptionResourceDecision {
	refs := bootstrap.MigratedAdoptionResourceRefs()
	decisions := make([]bootstrap.AdoptionResourceDecision, 0, len(refs))
	for _, ref := range refs {
		rule, ok := bootstrap.FreshAdoptionRule(ref)
		if !ok {
			panic("migrated ref without a rule: " + string(ref))
		}
		decisions = append(decisions, bootstrap.AdoptionResourceDecision{
			ResourceRef: ref, Kind: rule.Kind, Scope: rule.Scope,
			ReadEvidenceRef:   bootstrap.CapabilityEvidenceRef("preflight:" + string(ref) + "-read"),
			ReplayEvidenceRef: bootstrap.CapabilityEvidenceRef("preflight:" + string(ref) + "-replay"),
			VerifyEvidenceRef: bootstrap.CapabilityEvidenceRef("preflight:" + string(ref) + "-verify"),
			Qualification:     bootstrap.CapabilityQualified, Disposition: bootstrap.Migrate,
		})
	}
	return decisions
}

// acceptedKeyDecisionMap is the map form the inventory builder takes.
func acceptedKeyDecisionMap() map[bootstrap.ResourceRef]bootstrap.AdoptionResourceDecision {
	decisions, ok := qualifiedAdoptionDecisions(acceptedKeys())
	if !ok {
		panic("the accepted decision set is not qualified")
	}
	return decisions
}

type adoptionRuntime struct {
	managedRuntime
	mu               sync.Mutex
	stages           []model.RuntimeDestinationRequest
	lostStage        bool
	lostStart        bool
	wrongKeys        atomic.Bool
	wrongAuth        atomic.Bool
	changeGeneration bool
	statusCalls      int
	verifyTimeout    atomic.Bool
}

func (r *adoptionRuntime) Status(ctx context.Context) (model.RuntimeObservedStatus, error) {
	r.statusCalls++
	status := r.status
	if r.changeGeneration && r.statusCalls%2 == 0 {
		status.Generation++
	}
	return status, nil
}
func (r *adoptionRuntime) StageDestination(_ context.Context, request model.RuntimeDestinationRequest) (model.RuntimeOperationResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.stages) > 0 && !reflect.DeepEqual(r.stages[0], request) {
		return model.RuntimeOperationResult{}, errors.New("different stage")
	}
	r.stages = append(r.stages, request)
	if r.lostStage {
		r.lostStage = false
		return model.RuntimeOperationResult{}, errors.New("lost stage response")
	}
	return model.RuntimeOperationResult{OperationID: request.OperationID, OperationType: model.RuntimeOperationStageDestination, RuntimeIdentity: r.status.Identity, RuntimeGeneration: r.status.Generation, State: model.RuntimeOperationSucceeded}, nil
}
func (r *adoptionRuntime) Start(ctx context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	result, err := r.managedRuntime.Start(ctx, request)
	if r.lostStart {
		r.lostStart = false
		return model.RuntimeOperationResult{}, errors.New("lost start response")
	}
	return result, err
}
func (r *adoptionRuntime) target(w http.ResponseWriter, request *http.Request) {
	r.mu.Lock()
	if len(r.stages) == 0 {
		r.mu.Unlock()
		w.WriteHeader(503)
		return
	}
	stage := r.stages[0]
	r.mu.Unlock()
	if request.Method != "GET" {
		w.WriteHeader(405)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if request.URL.Path == "/v8/management/config" {
		if r.verifyTimeout.Load() {
			<-request.Context().Done()
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+stage.ManagementKey {
			w.WriteHeader(401)
			return
		}
		keys := stage.APIKeys
		if r.wrongKeys.Load() {
			keys = []string{"other-target-key"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"config-version": 8, "server": map[string]int{"port": 8317}, "oauth": map[string]string{"auth-dir": "destination-auth"}, "access": map[string]any{"api-keys": keys}})
		return
	}
	if request.URL.Path == "/v1/models" {
		for _, key := range stage.APIKeys {
			if request.Header.Get("Authorization") == "Bearer "+key {
				_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
				return
			}
		}
		if r.wrongAuth.Load() {
			_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
			return
		}
		w.WriteHeader(401)
		return
	}
	w.WriteHeader(404)
}

func TestAdoptionStagesEncryptedSnapshotBeforeDestinationAndVerifiesActualAuth(t *testing.T) {
	for _, class := range []bootstrap.AdoptSourceClass{bootstrap.AdoptNetworkExisting, bootstrap.AdoptLegacyManaged} {
		t.Run(string(class), func(t *testing.T) {
			sourceReads := 0
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path, body := "/v8/management/config", `{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"source-auth"},"access":{"api-keys":["source-client-one","source-client-two"]},"providers":{"secret":"never-migrate"},"usage":{"history":"never-migrate"}}`
				if class == bootstrap.AdoptLegacyManaged {
					path, body = "/v0/management/config", `{"tls":{"enable":false},"routing":{"strategy":"round-robin"},"usage-statistics-enabled":false,"gemini-api-key":[{"api-key":"never-migrate-provider"}],"api-keys":["source-client-one","source-client-two"]}`
				}
				if r.Method != "GET" || r.URL.Path != path || r.Header.Get("Authorization") != "Bearer source-management" {
					t.Error("source lifecycle/history/config mutated or cross-generation read")
					w.WriteHeader(403)
					return
				}
				sourceReads++
				w.Header().Set("Content-Type", "application/json")
				if class == bootstrap.AdoptLegacyManaged {
					w.Header().Set("X-CPA-VERSION", "7.0.12")
				}
				_, _ = w.Write([]byte(body))
			}))
			defer source.Close()
			artifact := model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64))
			runtime := &adoptionRuntime{managedRuntime: managedRuntime{status: model.RuntimeObservedStatus{Identity: "destination-runtime", Generation: 3, ProtocolVersion: "v1", State: model.RuntimeStateReady, Capabilities: model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStageDestination}, ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: artifact}}}}
			target := httptest.NewServer(http.HandlerFunc(runtime.target))
			defer target.Close()
			protector, _ := security.NewProtector([]byte("test-only-adoption-data-key"))
			q := qualified()
			options := Options{Runtime: runtime, Protector: protector, Qualification: q, QualifiedArtifactID: artifact, AdoptionResources: acceptedKeys(), DestinationBaseURL: target.URL}
			r := Request{Strategy: bootstrap.AdoptExisting, BaseURL: source.URL, ManagementKey: "source-management", ExpectedRuntimeIdentity: runtime.status.Identity, ExpectedRuntimeGeneration: 3, ExpectedArtifactID: artifact, AdoptSourceClass: class}
			if class == bootstrap.AdoptLegacyManaged {
				if _, err := New(options).Execute(t.Context(), operation(), r); err == nil || sourceReads != 0 {
					t.Fatal("missing legacy transition touched source")
				}
				options.Qualification.StagingV8TransitionQualified = true
			}
			op := operation()
			staged, err := New(options).Execute(t.Context(), op, r)
			if err != nil || staged.Checkpoint.Stage != "staged" || !security.IsValidProtectedEnvelope(staged.AdoptionCiphertext) || len(runtime.stages) != 0 || len(runtime.starts) != 0 || sourceReads != 1 {
				t.Fatalf("stage did effects: %v", err)
			}
			// The record covers every reference an item under the reported roots can
			// carry, not one reference per root. This source reports `server`,
			// `access`, `oauth`, `providers` and `usage`; each contributes the
			// reference of its own root plus everything its paths classify as, and
			// a root may contribute more than one (`client` carries both its
			// gateway leaves and its provider section). The v7 source reports
			// `access`, `oauth`, `server`, `routing` and `usage`.
			wantResources := []bootstrap.ResourceRef{
				bootstrap.AdoptionRefConfigServer, bootstrap.AdoptionRefConfigUnknown,
				bootstrap.AdoptionRefClientAPIKeys, bootstrap.AdoptionRefCPAAuthFiles,
				bootstrap.AdoptionRefProviderStateAPIKeys, bootstrap.AdoptionRefProviderStateProviders,
				bootstrap.AdoptionRefConfigUsage,
			}
			if class == bootstrap.AdoptLegacyManaged {
				wantResources = []bootstrap.ResourceRef{
					bootstrap.AdoptionRefClientAPIKeys, bootstrap.AdoptionRefConfigUnknown,
					bootstrap.AdoptionRefCPAAuthFiles, bootstrap.AdoptionRefProviderStateAPIKeys,
					bootstrap.AdoptionRefConfigServer, bootstrap.AdoptionRefConfigRouting,
					bootstrap.AdoptionRefConfigUsage, bootstrap.AdoptionRefProviderStateProviders,
				}
			}
			if !reflect.DeepEqual(staged.Checkpoint.AdoptionResources, wantResources) {
				t.Fatalf("discovered source inventory was not bounded: %+v", staged.Checkpoint.AdoptionResources)
			}
			wantDecisions, ok := adoptionDecisionsForInventory(wantResources, acceptedKeyDecisionMap())
			if !ok || !reflect.DeepEqual(staged.Checkpoint.AdoptionDecisions, wantDecisions) {
				t.Fatalf("discovered source resources were not explicitly decided: %+v", staged.Checkpoint.AdoptionDecisions)
			}
			migrated := 0
			migratedRefs := make([]bootstrap.ResourceRef, 0, len(staged.Checkpoint.AdoptionDecisions))
			for _, decision := range staged.Checkpoint.AdoptionDecisions {
				if decision.Disposition == bootstrap.Migrate {
					migrated++
					migratedRefs = append(migratedRefs, decision.ResourceRef)
					continue
				}
				if decision.ResourceRef == bootstrap.AdoptionRefClientAPIKeys || decision.ManualAction == "" || decision.Qualification != bootstrap.CapabilityPending {
					t.Fatalf("discovered resource silently dropped or migrated: %+v", decision)
				}
			}
			// The migrated set is exactly the accepted evidence's kinds that this
			// source actually carries, and it can never exceed the rules' migrated
			// set.
			authorized := map[bootstrap.ResourceRef]bool{}
			for _, decision := range acceptedKeys() {
				authorized[decision.ResourceRef] = true
			}
			wantMigrated := make([]bootstrap.ResourceRef, 0, len(acceptedKeys()))
			for _, ref := range wantResources {
				if authorized[ref] {
					wantMigrated = append(wantMigrated, ref)
				}
			}
			slices.Sort(wantMigrated)
			slices.Sort(migratedRefs)
			if !slices.Equal(migratedRefs, wantMigrated) {
				t.Fatalf("migrated resources %v, evidence authorizes %v", migratedRefs, wantMigrated)
			}
			if migrated != len(wantMigrated) {
				t.Fatal("client API keys were not part of the migrated set")
			}
			public, _ := json.Marshal(staged)
			for _, secret := range []string{"source-client", "source-management", "never-migrate", "enc:v1"} {
				if strings.Contains(string(public), secret) {
					t.Fatal("secret escaped checkpoint")
				}
			}
			op.Strategy = staged.Checkpoint
			r.AdoptionCiphertext = staged.AdoptionCiphertext
			// Reconstruct service each time, as after a Manager restart. Source
			// changes or absence cannot change the already frozen replay set.
			runtime.lostStage = true
			if _, err = New(options).Execute(t.Context(), op, r); err == nil {
				t.Fatal("lost stage reply committed")
			}
			runtime.lostStart = true
			if _, err = New(options).Execute(t.Context(), op, r); err == nil {
				t.Fatal("lost Start reply committed")
			}
			runtime.wrongKeys.Store(true)
			if _, err = New(options).Execute(t.Context(), op, r); err == nil {
				t.Fatal("different destination keys committed")
			}
			runtime.wrongKeys.Store(false)
			runtime.wrongAuth.Store(true)
			if _, err = New(options).Execute(t.Context(), op, r); err == nil {
				t.Fatal("persisted keys masked effective auth fail-open")
			}
			runtime.wrongAuth.Store(false)
			runtime.statusCalls = 0
			runtime.changeGeneration = true
			if _, err = New(options).Execute(t.Context(), op, r); err == nil {
				t.Fatal("generation changed during verify committed")
			}
			runtime.changeGeneration = false
			runtime.verifyTimeout.Store(true)
			timeout, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
			if result, err := New(options).Execute(timeout, op, r); err == nil || result.Checkpoint.Stage == "committed" {
				t.Fatal("timed-out destination committed")
			}
			cancel()
			runtime.verifyTimeout.Store(false)
			committed, err := New(options).Execute(t.Context(), op, r)
			if err != nil || committed.Checkpoint.Stage != "committed" || committed.Connection == nil || !committed.GatewayPending || sourceReads != 1 {
				t.Fatalf("verify: %v", err)
			}
			for _, request := range runtime.stages {
				if !reflect.DeepEqual(request, runtime.stages[0]) {
					t.Fatal("retry changed frozen replay")
				}
			}
			if runtime.stages[0].OperationID != "fresh-operation-123:adopt-destination" || runtime.starts[0].OperationID != "fresh-operation-123:adopt-destination:start" {
				t.Fatal("destination did not reuse the same Fresh child operation IDs")
			}
			op.Strategy = committed.Checkpoint
			plainSnapshot, _ := protector.UnprotectString(r.AdoptionCiphertext)
			stageCalls, startCalls := len(runtime.stages), len(runtime.starts)
			if _, err = New(options).Execute(t.Context(), op, r); err != nil || len(runtime.stages) != stageCalls || len(runtime.starts) != startCalls {
				t.Fatal("committed replay mutated target")
			}
			for _, mutate := range []func(*Request){func(r *Request) { r.BaseURL = target.URL }, func(r *Request) { r.ManagementKey = "different" }, func(r *Request) { r.ExpectedRuntimeGeneration++ }, func(r *Request) { r.ExpectedRuntimeIdentity = "other" }, func(r *Request) { r.ExpectedArtifactID = model.RuntimeArtifactID("sha256:" + strings.Repeat("b", 64)) }, func(r *Request) {
				if r.AdoptSourceClass == bootstrap.AdoptNetworkExisting {
					r.AdoptSourceClass = bootstrap.AdoptLegacyManaged
				} else {
					r.AdoptSourceClass = bootstrap.AdoptNetworkExisting
				}
			}} {
				changed := r
				mutate(&changed)
				if _, err = New(options).Execute(t.Context(), op, changed); err == nil {
					t.Fatal("conflicting replay accepted")
				}
			}
			if sourceReads != 1 {
				t.Fatal("replay rediscovered source")
			}
			plainRequest := r
			plainRequest.AdoptionCiphertext = plainSnapshot
			if _, err = New(options).Execute(t.Context(), op, plainRequest); err == nil {
				t.Fatal("plaintext resource snapshot accepted")
			}
		})
	}
}

// A source that authenticates but carries no client Key has nothing to adopt,
// and adopting it would install a destination whose client authentication is
// disabled. The refusal is its own typed blocker answered by that single source
// GET, so it happens before any destination operation or checkpoint mutation and
// no destination operation is issued.
func TestAdoptionRefusesAZeroKeySourceBeforeAnyDestinationOperation(t *testing.T) {
	sourceReads := 0
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v8/management/config" || r.Header.Get("Authorization") != "Bearer source-management" {
			t.Error("zero-key refusal reached an unexpected source surface")
			w.WriteHeader(403)
			return
		}
		sourceReads++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"source-auth"},"access":{"api-keys":[]}}`))
	}))
	defer source.Close()
	artifact := model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64))
	runtime := &adoptionRuntime{managedRuntime: managedRuntime{status: model.RuntimeObservedStatus{Identity: "destination-runtime", Generation: 3, ProtocolVersion: "v1", State: model.RuntimeStateReady, Capabilities: model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStageDestination}, ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: artifact}}}}
	protector, _ := security.NewProtector([]byte("test-only-adoption-data-key"))
	options := Options{Runtime: runtime, Protector: protector, Qualification: qualified(), QualifiedArtifactID: artifact, AdoptionResources: acceptedKeys(), DestinationBaseURL: "http://127.0.0.1:1"}
	request := Request{Strategy: bootstrap.AdoptExisting, BaseURL: source.URL, ManagementKey: "source-management", ExpectedRuntimeIdentity: runtime.status.Identity, ExpectedRuntimeGeneration: 3, ExpectedArtifactID: artifact, AdoptSourceClass: bootstrap.AdoptNetworkExisting}
	result, err := New(options).Execute(t.Context(), operation(), request)
	var blocker *Blocker
	if !errors.As(err, &blocker) || blocker.Code != "adoption_source_empty" {
		t.Fatalf("zero-key source: %+v %v", result, err)
	}
	if sourceReads != 1 || result.AdoptionCiphertext != "" || result.Checkpoint.Strategy != "" || !result.GatewayPending || !result.SourcePreserved {
		t.Fatalf("zero-key refusal had effects: reads=%d %+v", sourceReads, result)
	}
	if len(runtime.stages) != 0 || len(runtime.starts) != 0 {
		t.Fatal("zero-key refusal staged or started a destination")
	}
}

// Adopting a v7 or Manager-managed source is Upgrade's path, and an unspecified
// class never guesses which generation to read. Both are refused with their own
// code before the source is touched and before the destination moves, while the
// admitted class proceeds to the adapter.
func TestAdoptionRefusesLegacyAndUnspecifiedSourceClasses(t *testing.T) {
	var sourceReads atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceReads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"source-auth"},"access":{"api-keys":["source-client"]}}`))
	}))
	defer source.Close()
	artifact := model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64))
	runtime := &adoptionRuntime{managedRuntime: managedRuntime{status: model.RuntimeObservedStatus{Identity: "destination-runtime", Generation: 3, ProtocolVersion: "v1", State: model.RuntimeStateReady, Capabilities: model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStageDestination}, ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: artifact}}}}
	protector, _ := security.NewProtector([]byte("test-only-adoption-data-key"))
	options := Options{Runtime: runtime, Protector: protector, Qualification: qualified(), QualifiedArtifactID: artifact, AdoptionResources: acceptedKeys(), DestinationBaseURL: "http://127.0.0.1:1"}
	for _, item := range []struct {
		class   bootstrap.AdoptSourceClass
		blocker string
	}{
		// Legacy adoption needs its own accepted staging transition, which this
		// fixture does not carry.
		{bootstrap.AdoptLegacyManaged, "adoption_source_qualification_required"},
		// The adapter is the backstop for a class the HTTP admission already
		// refuses: no qualification is scoped to a missing class, so it never
		// guesses a generation to read.
		{bootstrap.AdoptSourceNone, "adoption_source_qualification_required"},
	} {
		request := Request{Strategy: bootstrap.AdoptExisting, BaseURL: source.URL, ManagementKey: "source-management", ExpectedRuntimeIdentity: runtime.status.Identity, ExpectedRuntimeGeneration: 3, ExpectedArtifactID: artifact, AdoptSourceClass: item.class}
		result, err := New(options).Execute(t.Context(), operation(), request)
		var blocker *Blocker
		if !errors.As(err, &blocker) || blocker.Code != item.blocker {
			t.Fatalf("class %q: %+v %v", item.class, result, err)
		}
		if result.Checkpoint.Strategy != "" || result.AdoptionCiphertext != "" {
			t.Fatalf("class %q persisted a checkpoint", item.class)
		}
	}
	// Neither refusal is the empty-source reason: the class itself is refused,
	// so the source is never read and the source reader is never reached.
	if sourceReads.Load() != 0 || len(runtime.stages) != 0 || len(runtime.starts) != 0 {
		t.Fatalf("refused class touched source or destination: reads=%d", sourceReads.Load())
	}
}
