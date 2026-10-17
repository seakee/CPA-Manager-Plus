package freshstrategy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

func TestAdoptionSourceQualificationPrecedesAllEffectsAcrossRetries(t *testing.T) {
	var calls atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		t.Errorf("blocked adoption touched source: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer source.Close()
	for _, class := range []bootstrap.AdoptSourceClass{bootstrap.AdoptNetworkExisting, bootstrap.AdoptLegacyManaged, bootstrap.AdoptSourceNone, "unknown"} {
		for _, profile := range []bool{false, true} {
			for _, transition := range []bool{false, true} {
				name := string(class)
				if profile {
					name += "/profile"
				}
				if transition {
					name += "/transition"
				}
				t.Run(name, func(t *testing.T) {
					q := bootstrap.CPAQualification{ManagementGeneration: "v8", RequiredProfileQualified: profile, StagingV8TransitionQualified: transition}
					want := "adoption_source_qualification_required"
					if profile && (class == bootstrap.AdoptNetworkExisting || class == bootstrap.AdoptLegacyManaged && transition) {
						want = "adoption_destination_staging_required"
					}
					original := operation()
					encoded, err := json.Marshal(original)
					if err != nil {
						t.Fatal(err)
					}
					for attempt := 0; attempt < 3; attempt++ {
						// Reconstruct the service and operation as after restart or a
						// lost reply. No checkpoint may be invented from either.
						var op bootstrap.FreshOperation
						if err := json.Unmarshal(encoded, &op); err != nil {
							t.Fatal(err)
						}
						result, err := New(Options{Qualification: q}).Execute(context.Background(), op, Request{
							Strategy: bootstrap.AdoptExisting, AdoptSourceClass: class,
							BaseURL: source.URL, ManagementKey: "private-source-secret",
						})
						var blocker *Blocker
						if !errors.As(err, &blocker) || blocker.Code != want || !result.SourcePreserved || !result.GatewayPending || result.Checkpoint.Strategy != "" || result.Connection != nil || !reflect.DeepEqual(op, original) {
							t.Fatalf("attempt %d: result=%+v err=%v", attempt, result, err)
						}
					}
				})
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatal("qualification/destination blocker did not preserve source")
	}
}

func TestManagedStagedIntentCannotRestoreWithdrawnQualification(t *testing.T) {
	artifact := model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64))
	runtime := &managedRuntime{status: model.RuntimeObservedStatus{
		Identity: "destination", Generation: 7, ProtocolVersion: "v1", State: model.RuntimeStateReady,
		CPAObservedVersion:    "8.0.13",
		Capabilities:          model.RuntimeCapabilities{model.RuntimeCapabilityStart},
		ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: artifact, Version: "8.0.13"},
	}}
	if err := runtime.status.Validate(); err != nil {
		t.Fatal(err)
	}
	op := operation()
	op.Strategy = bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, TargetIdentity: "destination", TargetGeneration: 7, TargetArtifactID: string(artifact), Stage: "staged"}
	for _, item := range []struct {
		name     string
		q        bootstrap.CPAQualification
		artifact model.RuntimeArtifactID
	}{
		{"missing", bootstrap.CPAQualification{}, artifact},
		{"withdrawn artifact", bootstrap.CPAQualification{ManagementGeneration: "v8", RequiredProfileQualified: true}, artifact},
		{"wrong generation", bootstrap.CPAQualification{ManagementGeneration: "v0", ArtifactQualified: true}, artifact},
		{"missing exact identity", qualified(), ""},
		{"malformed exact identity", qualified(), "sha256:invalid"},
		{"different exact identity", qualified(), model.RuntimeArtifactID("sha256:" + strings.Repeat("b", 64))},
	} {
		t.Run(item.name, func(t *testing.T) {
			result, err := New(Options{Runtime: runtime, Qualification: item.q, QualifiedArtifactID: item.artifact}).Execute(context.Background(), op, Request{Strategy: bootstrap.NewManaged})
			var blocker *Blocker
			if !errors.As(err, &blocker) || blocker.Code != "exact_v8_artifact_qualification_required" || result.Checkpoint.Stage == "committed" || len(runtime.starts) != 0 {
				t.Fatalf("durable intent/runtime observation restored qualification: %+v %v", result, err)
			}
		})
	}
}
