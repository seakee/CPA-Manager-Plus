package cpaupdate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

type updateObservationRuntime struct {
	observationOnlyRuntime
	observeFn    func(context.Context, model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error)
	observeCalls int
	requests     []model.RuntimeObserveUpdateOperationRequest
}

func (r *updateObservationRuntime) ObserveUpdateOperation(ctx context.Context, request model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
	r.observeCalls++
	r.requests = append(r.requests, request)
	return r.observeFn(ctx, request)
}

type forbiddenObservationStore struct{}

func (forbiddenObservationStore) LoadCPAUpdateCheck(context.Context) ([]byte, error) {
	panic("operation observation must not read recommendation persistence")
}

func (forbiddenObservationStore) SaveCPAUpdateCheck(context.Context, []byte) error {
	panic("operation observation must not write recommendation persistence")
}

func updateObservationService(t *testing.T) (*Service, *updateObservationRuntime, *fakeSource) {
	t.Helper()
	client := &updateObservationRuntime{
		observationOnlyRuntime: observationOnlyRuntime{
			status: embeddedObservation("7.3.4", model.RuntimeCapabilityObserveUpdateOperation),
		},
		observeFn: func(_ context.Context, request model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
			return retainedUpdateObservation(request, model.RuntimeOperationSucceeded), nil
		},
	}
	client.status.Generation = 42
	source := &fakeSource{err: errors.New("observation must not discover releases")}
	service := New(forbiddenObservationStore{}, client, model.RuntimeModeEmbedded)
	service.source = source
	return service, client, source
}

func retainedUpdateObservation(request model.RuntimeObserveUpdateOperationRequest, state model.RuntimeOperationState) model.RuntimeUpdateOperationObservation {
	created := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	result := model.RuntimeUpdateOperationObservation{
		OperationID: request.OperationID, OperationType: request.OperationType,
		RuntimeIdentity: request.ExpectedRuntimeIdentity, RuntimeGeneration: 41,
		State: state, CreatedAt: created, UpdatedAt: created.Add(time.Second),
	}
	if state == model.RuntimeOperationSucceeded || state == model.RuntimeOperationFailed {
		completed := result.UpdatedAt
		result.CompletedAt = &completed
	}
	if state == model.RuntimeOperationFailed {
		result.FailureCode = "update_stage_failed"
	}
	return result
}

func TestObserveUpdateNormalizesEveryPhaseAndDurableState(t *testing.T) {
	for _, phase := range []MutationPhase{MutationPhasePrepare, MutationPhaseActivate} {
		for _, state := range []model.RuntimeOperationState{
			model.RuntimeOperationAccepted, model.RuntimeOperationRunning,
			model.RuntimeOperationSucceeded, model.RuntimeOperationFailed,
		} {
			t.Run(string(phase)+"/"+string(state), func(t *testing.T) {
				service, client, source := updateObservationService(t)
				client.observeFn = func(ctx context.Context, request model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
					if ctx != t.Context() {
						t.Fatal("observation lost caller context")
					}
					return retainedUpdateObservation(request, state), nil
				}
				request := ObservationRequest{RequestID: "web-0192ac", TargetVersion: "7.3.4"}
				var got ObservationResult
				var err error
				for attempt := 0; attempt < 2; attempt++ {
					if phase == MutationPhasePrepare {
						got, err = service.ObservePrepare(t.Context(), request)
					} else {
						got, err = service.ObserveActivate(t.Context(), request)
					}
					if err != nil || got.State != ObservationState(state) || got.Phase != phase ||
						got.RequestID != request.RequestID || got.TargetVersion != request.TargetVersion ||
						got.RuntimeOperationID != runtimeOperationID(phase, request.RequestID) {
						t.Fatalf("Observe() = %+v, %v", got, err)
					}
					sent := client.requests[attempt]
					wantType := model.RuntimeOperationPrepareUpdate
					if phase == MutationPhaseActivate {
						wantType = model.RuntimeOperationActivateUpdate
					}
					if sent.OperationID != got.RuntimeOperationID || sent.OperationType != wantType ||
						sent.ExpectedRuntimeIdentity != client.status.Identity || sent.ExpectedRuntimeGeneration != 42 ||
						sent.TargetVersion != request.TargetVersion {
						t.Fatalf("Runtime query = %+v", sent)
					}
					retained := retainedUpdateObservation(sent, state)
					if got.CreatedAt == nil || !got.CreatedAt.Equal(retained.CreatedAt) ||
						got.UpdatedAt == nil || !got.UpdatedAt.Equal(retained.UpdatedAt) ||
						!reflect.DeepEqual(got.CompletedAt, retained.CompletedAt) || got.FailureCode != retained.FailureCode {
						t.Fatalf("lost durable evidence: %+v", got)
					}
				}
				if client.statusCalls.Load() != 2 || client.observeCalls != 2 || source.requests.Load() != 0 {
					t.Fatalf("calls status=%d observe=%d discovery=%d", client.statusCalls.Load(), client.observeCalls, source.requests.Load())
				}
			})
		}
	}
}

func TestObserveUpdateIgnoresMutationAdmission(t *testing.T) {
	tests := map[string]func(*Service, *updateObservationRuntime){
		"stale recommendation": func(s *Service, _ *updateObservationRuntime) {
			s.loaded = true
			s.state = DiscoveryState{SchemaVersion: 1, TargetVersion: "7.3.4",
				LastAttemptAt: time.Now().Add(-48 * time.Hour), LastSuccessAt: time.Now().Add(-48 * time.Hour)}
		},
		"discovery last_error": func(s *Service, _ *updateObservationRuntime) {
			s.loaded = true
			s.state.LastError = "release discovery failed: private token"
		},
		"recommendation persistence unavailable": func(s *Service, _ *updateObservationRuntime) { s.persistenceFailed = true },
		"starting Runtime":                       func(_ *Service, r *updateObservationRuntime) { r.status.State = model.RuntimeStateStarting },
		"offline Runtime":                        func(_ *Service, r *updateObservationRuntime) { r.status.State = model.RuntimeStateOffline },
		"unknown Runtime":                        func(_ *Service, r *updateObservationRuntime) { r.status.State = model.RuntimeStateUnknown },
		"active artifact changed": func(_ *Service, r *updateObservationRuntime) {
			r.status.ActiveGatewayArtifact.ArtifactID = testArtifactIDB
		},
		"active artifact missing": func(_ *Service, r *updateObservationRuntime) {
			r.status.ActiveGatewayArtifact = nil
			r.status.CPAObservedVersion = ""
		},
		"current version ahead of target": func(_ *Service, r *updateObservationRuntime) {
			r.status.CPAObservedVersion = "8.0.0"
			r.status.ActiveGatewayArtifact.Version = "8.0.0"
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			service, client, source := updateObservationService(t)
			change(service, client)
			got, err := service.ObserveActivate(t.Context(), ObservationRequest{RequestID: "web-recover", TargetVersion: "7.3.4"})
			if err != nil || got.State != ObservationStateSucceeded {
				t.Fatalf("ObserveActivate() = %+v, %v", got, err)
			}
			if client.statusCalls.Load() != 1 || client.observeCalls != 1 || source.requests.Load() != 0 {
				t.Fatalf("calls status=%d observe=%d discovery=%d", client.statusCalls.Load(), client.observeCalls, source.requests.Load())
			}
		})
	}
}

func TestObserveUpdateRefreshesOpaqueGenerationWithoutComparingCreationEpoch(t *testing.T) {
	service, client, _ := updateObservationService(t)
	for _, generation := range []model.RuntimeGeneration{42, 7, 1 << 63} {
		client.status.Generation = generation
		got, err := service.ObservePrepare(t.Context(), ObservationRequest{RequestID: "retained", TargetVersion: "7.3.4"})
		if err != nil || got.State != ObservationStateSucceeded {
			t.Fatalf("current generation %d = %+v, %v", generation, got, err)
		}
		if client.requests[len(client.requests)-1].ExpectedRuntimeGeneration != generation {
			t.Fatal("query reused cached Runtime authority")
		}
	}
}

func TestObserveUpdateNotFoundNeverInfersSuccessOrStartsWork(t *testing.T) {
	service, client, source := updateObservationService(t)
	client.observeFn = func(context.Context, model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
		return model.RuntimeUpdateOperationObservation{}, &runtimeservice.ProtocolError{
			Code: runtimeservice.ProtocolErrorOperationNotFound, HTTPStatus: http.StatusNotFound,
		}
	}
	// Current version already equals the target, but there is no durable intent.
	got, err := service.ObserveActivate(t.Context(), ObservationRequest{RequestID: "lost-before-begin", TargetVersion: "7.3.4"})
	if err != nil || got.State != ObservationStateNotFound || got.CreatedAt != nil || got.UpdatedAt != nil ||
		got.CompletedAt != nil || got.FailureCode != "" || source.requests.Load() != 0 || client.observeCalls != 1 {
		t.Fatalf("missing operation = %+v, %v", got, err)
	}
	wire, err := json.Marshal(got)
	if err != nil || strings.Contains(string(wire), "already_applied") {
		t.Fatalf("not_found response = %s, %v", wire, err)
	}
}

func TestObserveUpdateFailsClosedAndRedactsRuntimeErrors(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Service, *updateObservationRuntime)
		kind   ObservationErrorKind
		code   string
		calls  int
	}{
		{"external", func(s *Service, _ *updateObservationRuntime) { s.mode = model.RuntimeModeExternal }, ObservationErrorConflict, "managed_externally", 0},
		{"invalid mode", func(s *Service, _ *updateObservationRuntime) { s.mode = "unknown" }, ObservationErrorUnavailable, "runtime_mode_invalid", 0},
		{"missing Runtime", func(s *Service, _ *updateObservationRuntime) { s.runtime = nil }, ObservationErrorUnavailable, "runtime_unavailable", 0},
		{"status unavailable", func(_ *Service, r *updateObservationRuntime) {
			r.statusErr = errors.New("/private/runtime token=secret")
		}, ObservationErrorUnavailable, "runtime_observation_unavailable", 0},
		{"missing identity", func(_ *Service, r *updateObservationRuntime) { r.status.Identity = "" }, ObservationErrorUnavailable, "runtime_observation_invalid", 0},
		{"zero generation", func(_ *Service, r *updateObservationRuntime) { r.status.Generation = 0 }, ObservationErrorUnavailable, "runtime_observation_invalid", 0},
		{"missing capability", func(_ *Service, r *updateObservationRuntime) {
			r.status.Capabilities = model.RuntimeCapabilities{model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate}
		}, ObservationErrorConflict, "runtime_capability_unavailable", 0},
	}
	for _, protocol := range []struct {
		code   runtimeservice.ProtocolErrorCode
		status int
		kind   ObservationErrorKind
		want   string
	}{
		{runtimeservice.ProtocolErrorRuntimeIdentityMismatch, 409, ObservationErrorConflict, "runtime_identity_mismatch"},
		{runtimeservice.ProtocolErrorStaleRuntimeGeneration, 409, ObservationErrorConflict, "stale_runtime_generation"},
		{runtimeservice.ProtocolErrorOperationIDConflict, 409, ObservationErrorConflict, "operation_id_conflict"},
		{runtimeservice.ProtocolErrorUnsupportedOperation, 400, ObservationErrorConflict, "unsupported_operation"},
		{runtimeservice.ProtocolErrorOperationNotFound, 503, ObservationErrorUnavailable, "runtime_operation_unavailable"},
		{runtimeservice.ProtocolErrorOperationPersistenceUnavailable, 503, ObservationErrorUnavailable, "runtime_operation_unavailable"},
		{"/private/runtime token=secret", 500, ObservationErrorUnavailable, "runtime_operation_unavailable"},
	} {
		tests = append(tests, struct {
			name   string
			change func(*Service, *updateObservationRuntime)
			kind   ObservationErrorKind
			code   string
			calls  int
		}{string(protocol.code), func(_ *Service, r *updateObservationRuntime) {
			r.observeFn = func(context.Context, model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
				return model.RuntimeUpdateOperationObservation{}, &runtimeservice.ProtocolError{Code: protocol.code, HTTPStatus: protocol.status}
			}
		}, protocol.kind, protocol.want, 1})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, client, source := updateObservationService(t)
			test.change(service, client)
			got, err := service.ObservePrepare(t.Context(), ObservationRequest{RequestID: "recover", TargetVersion: "7.3.4"})
			kind, code, ok := ObservationErrorDetails(err)
			if !ok || kind != test.kind || code != test.code || !reflect.DeepEqual(got, ObservationResult{}) ||
				strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "/private/") {
				t.Fatalf("ObservePrepare() = %+v, %v; details=%s/%s", got, err, kind, code)
			}
			if client.observeCalls != test.calls || source.requests.Load() != 0 {
				t.Fatalf("observe=%d discovery=%d", client.observeCalls, source.requests.Load())
			}
			if test.name == "external" && client.statusCalls.Load() != 0 {
				t.Fatal("External observation contacted Runtime")
			}
		})
	}
}

func TestObserveUpdateRejectsUnreliableOperationEvidence(t *testing.T) {
	for name, change := range map[string]func(*model.RuntimeUpdateOperationObservation){
		"wrong ID": func(r *model.RuntimeUpdateOperationObservation) { r.OperationID = "another-op" },
		"wrong phase": func(r *model.RuntimeUpdateOperationObservation) {
			r.OperationType = model.RuntimeOperationActivateUpdate
		},
		"wrong identity":     func(r *model.RuntimeUpdateOperationObservation) { r.RuntimeIdentity = "another-runtime" },
		"unknown state":      func(r *model.RuntimeUpdateOperationObservation) { r.State = "already_applied" },
		"missing timestamps": func(r *model.RuntimeUpdateOperationObservation) { r.CreatedAt = time.Time{} },
		"raw failure": func(r *model.RuntimeUpdateOperationObservation) {
			r.State = model.RuntimeOperationFailed
			r.FailureCode = "/private/runtime token=secret"
		},
	} {
		t.Run(name, func(t *testing.T) {
			service, client, _ := updateObservationService(t)
			client.observeFn = func(_ context.Context, request model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
				result := retainedUpdateObservation(request, model.RuntimeOperationSucceeded)
				change(&result)
				return result, nil
			}
			got, err := service.ObservePrepare(t.Context(), ObservationRequest{RequestID: "recover", TargetVersion: "7.3.4"})
			_, code, ok := ObservationErrorDetails(err)
			if !ok || code != "runtime_result_unreliable" || got.State != "" {
				t.Fatalf("unreliable result = %+v, %v", got, err)
			}
		})
	}
}

func TestObservationRequestUsesMutationIdentityValidationBeforeRuntime(t *testing.T) {
	for _, request := range []ObservationRequest{
		{}, {RequestID: "has space", TargetVersion: "7.3.4"},
		{RequestID: strings.Repeat("a", 65), TargetVersion: "7.3.4"},
		{RequestID: "valid-id", TargetVersion: "latest"},
		{RequestID: "valid-id", TargetVersion: "v7.3.4"},
		{RequestID: "valid-id", TargetVersion: "7.3.4-rc.1"},
	} {
		service, client, source := updateObservationService(t)
		_, err := service.ObservePrepare(t.Context(), request)
		_, code, ok := ObservationErrorDetails(err)
		if !ok || code != "invalid_request" || client.statusCalls.Load() != 0 ||
			client.observeCalls != 0 || source.requests.Load() != 0 {
			t.Fatalf("invalid request %+v = %v", request, err)
		}
	}
}
