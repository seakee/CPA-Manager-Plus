package cpaupdate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

const testArtifactIDB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type mutationRuntime struct {
	mu sync.Mutex

	status    model.RuntimeObservedStatus
	statusErr error

	prepareFn  func(model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error)
	activateFn func(model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error)

	statusCalls   atomic.Int32
	prepareCalls  atomic.Int32
	activateCalls atomic.Int32

	lastPrepare  model.RuntimePrepareUpdateRequest
	lastActivate model.RuntimeActivateUpdateRequest
}

func (r *mutationRuntime) Status(context.Context) (model.RuntimeObservedStatus, error) {
	r.statusCalls.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status, r.statusErr
}

func (*mutationRuntime) Start(context.Context, model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	panic("unexpected Runtime Start mutation")
}

func (*mutationRuntime) Stop(context.Context, model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	panic("unexpected Runtime Stop mutation")
}

func (*mutationRuntime) Restart(context.Context, model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	panic("unexpected Runtime Restart mutation")
}

func (r *mutationRuntime) PrepareUpdate(_ context.Context, request model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
	r.prepareCalls.Add(1)
	r.mu.Lock()
	r.lastPrepare = request
	fn := r.prepareFn
	r.mu.Unlock()
	if fn != nil {
		return fn(request)
	}
	return successfulOperation(request.RuntimeMutationRequest, model.RuntimeOperationPrepareUpdate), nil
}

func (r *mutationRuntime) ActivateUpdate(_ context.Context, request model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
	r.activateCalls.Add(1)
	r.mu.Lock()
	r.lastActivate = request
	fn := r.activateFn
	r.mu.Unlock()
	if fn != nil {
		return fn(request)
	}
	return successfulOperation(request.RuntimeMutationRequest, model.RuntimeOperationActivateUpdate), nil
}

func successfulOperation(request model.RuntimeMutationRequest, operationType model.RuntimeOperationType) model.RuntimeOperationResult {
	return model.RuntimeOperationResult{
		OperationID:       request.OperationID,
		OperationType:     operationType,
		RuntimeIdentity:   request.ExpectedRuntimeIdentity,
		RuntimeGeneration: request.ExpectedRuntimeGeneration,
		State:             model.RuntimeOperationSucceeded,
	}
}

func (*mutationRuntime) ObserveUpdateOperation(context.Context, model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
	panic("mutation must not observe update operations")
}

func mutationTestService(t *testing.T, runtimeClient *mutationRuntime) (*Service, *fakeSource, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	source := &fakeSource{err: errors.New("mutation must not perform discovery")}
	service := New(persistedDiscovery(t, "7.3.7", now), runtimeClient, model.RuntimeModeEmbedded)
	service.source = source
	service.now = func() time.Time { return now }
	return service, source, now
}

func mutationReadyStatus(version string, artifactID string, capabilities ...model.RuntimeCapability) model.RuntimeObservedStatus {
	status := embeddedObservation(version, capabilities...)
	status.Identity = "runtime-mutation-test"
	status.Generation = 42
	status.ActiveGatewayArtifact.ArtifactID = model.RuntimeArtifactID(artifactID)
	return status
}

func validMutationRequest() MutationRequest {
	return MutationRequest{
		RequestID:                "web-0192ac",
		TargetVersion:            "7.3.7",
		ExpectedActiveArtifactID: testArtifactID,
	}
}

func TestMutationRequestIDValidation(t *testing.T) {
	request := validMutationRequest()
	for _, requestID := range []string{"update-123", "web_abc.001", "req:abcd"} {
		request.RequestID = requestID
		if err := request.Validate(); err != nil {
			t.Fatalf("Validate(%q) error = %v", requestID, err)
		}
	}
	for _, requestID := range []string{
		"", " update-123", "update-123 ", "hello world", "foo/bar", `foo\bar`, "foo\nbar",
		"更新", "control\x00id", strings.Repeat("a", maxMutationRequestIDBytes+1),
	} {
		request.RequestID = requestID
		if err := request.Validate(); err == nil {
			t.Fatalf("Validate(%q) accepted invalid request ID", requestID)
		}
	}
}

func TestValidPrepareAndActivateUseFreshExactFences(t *testing.T) {
	for _, phase := range []MutationPhase{MutationPhasePrepare, MutationPhaseActivate} {
		t.Run(string(phase), func(t *testing.T) {
			runtimeClient := &mutationRuntime{status: mutationReadyStatus(
				"7.3.3",
				testArtifactID,
				model.RuntimeCapabilityPrepareUpdate,
				model.RuntimeCapabilityActivateUpdate,
			)}
			service, source, _ := mutationTestService(t, runtimeClient)
			request := validMutationRequest()

			var result MutationResult
			var err error
			if phase == MutationPhasePrepare {
				result, err = service.Prepare(t.Context(), request)
			} else {
				result, err = service.Activate(t.Context(), request)
			}
			if err != nil {
				t.Fatalf("%s error = %v", phase, err)
			}
			if result.State != string(model.RuntimeOperationSucceeded) || result.AlreadyApplied || result.FailureCode != "" {
				t.Fatalf("%s result = %#v", phase, result)
			}
			if source.requests.Load() != 0 || runtimeClient.statusCalls.Load() != 1 {
				t.Fatalf("source calls = %d, status calls = %d", source.requests.Load(), runtimeClient.statusCalls.Load())
			}
			if phase == MutationPhasePrepare {
				if runtimeClient.prepareCalls.Load() != 1 || runtimeClient.activateCalls.Load() != 0 {
					t.Fatalf("prepare calls = %d, activate calls = %d", runtimeClient.prepareCalls.Load(), runtimeClient.activateCalls.Load())
				}
				got := runtimeClient.lastPrepare
				if got.ExpectedRuntimeIdentity != "runtime-mutation-test" || got.ExpectedRuntimeGeneration != 42 ||
					got.ExpectedActiveArtifactID != testArtifactID || got.TargetVersion != "7.3.7" ||
					got.OperationID != runtimeOperationID(phase, request.RequestID) {
					t.Fatalf("PrepareUpdate request = %#v", got)
				}
			} else {
				if runtimeClient.activateCalls.Load() != 1 || runtimeClient.prepareCalls.Load() != 0 {
					t.Fatalf("activate calls = %d, prepare calls = %d", runtimeClient.activateCalls.Load(), runtimeClient.prepareCalls.Load())
				}
				got := runtimeClient.lastActivate
				if got.ExpectedRuntimeIdentity != "runtime-mutation-test" || got.ExpectedRuntimeGeneration != 42 ||
					got.ExpectedActiveArtifactID != testArtifactID || got.TargetVersion != "7.3.7" ||
					got.OperationID != runtimeOperationID(phase, request.RequestID) {
					t.Fatalf("ActivateUpdate request = %#v", got)
				}
			}
		})
	}
}

func TestRuntimeOperationIDIsDeterministicAndExcludesTarget(t *testing.T) {
	requestID := "req:abcd"
	prepare := runtimeOperationID(MutationPhasePrepare, requestID)
	if prepare != runtimeOperationID(MutationPhasePrepare, requestID) {
		t.Fatal("same request ID produced a different prepare operation ID")
	}
	if prepare == runtimeOperationID(MutationPhaseActivate, requestID) {
		t.Fatal("prepare and activate operation IDs are equal")
	}
	if prepare != newMutationResult(MutationPhasePrepare, MutationRequest{RequestID: requestID, TargetVersion: "7.3.7"}).RuntimeOperationID ||
		prepare != newMutationResult(MutationPhasePrepare, MutationRequest{RequestID: requestID, TargetVersion: "7.3.8"}).RuntimeOperationID {
		t.Fatal("target version changed the operation ID")
	}
	if strings.Contains(prepare, requestID) || !strings.HasPrefix(prepare, "manager-cpa-update/v1:prepare:") || len(strings.TrimPrefix(prepare, "manager-cpa-update/v1:prepare:")) != 64 {
		t.Fatalf("operation ID shape = %q", prepare)
	}
	request := validMutationRequest()
	request.RequestID = requestID
	firstRuntime := &mutationRuntime{status: mutationReadyStatus("7.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate)}
	firstService, _, now := mutationTestService(t, firstRuntime)
	firstResult, err := firstService.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	retryResult, err := firstService.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	secondRuntime := &mutationRuntime{status: mutationReadyStatus("7.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate)}
	secondService, _, _ := mutationTestService(t, secondRuntime)
	restartedResult, err := secondService.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	changedRuntime := &mutationRuntime{status: mutationReadyStatus("7.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate)}
	changedService := New(persistedDiscovery(t, "7.3.8", now), changedRuntime, model.RuntimeModeEmbedded)
	changedService.now = func() time.Time { return now }
	changedRequest := request
	changedRequest.TargetVersion = "7.3.8"
	changedResult, err := changedService.Prepare(t.Context(), changedRequest)
	if err != nil {
		t.Fatal(err)
	}
	if firstResult.RuntimeOperationID != retryResult.RuntimeOperationID ||
		firstResult.RuntimeOperationID != restartedResult.RuntimeOperationID ||
		firstResult.RuntimeOperationID != changedResult.RuntimeOperationID {
		t.Fatalf("operation IDs changed: first=%q retry=%q restart=%q target=%q",
			firstResult.RuntimeOperationID, retryResult.RuntimeOperationID, restartedResult.RuntimeOperationID, changedResult.RuntimeOperationID)
	}
}

func TestMutationAdmissionRejectsWithoutRuntimeMutationOrDiscovery(t *testing.T) {
	now := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	ready := func() model.RuntimeObservedStatus {
		return mutationReadyStatus("7.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate)
	}
	tests := []struct {
		name       string
		phase      MutationPhase
		mode       model.RuntimeMode
		request    MutationRequest
		state      DiscoveryState
		status     model.RuntimeObservedStatus
		statusErr  error
		wantKind   MutationErrorKind
		wantStatus int32
	}{
		{name: "stale recommendation", phase: MutationPhasePrepare, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now.Add(-discoveryFreshness - time.Second), LastSuccessAt: now.Add(-discoveryFreshness - time.Second), TargetVersion: "7.3.7"}, status: ready(), wantKind: MutationErrorConflict},
		{name: "discovery error", phase: MutationPhaseActivate, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now.Add(-time.Minute), LastError: "failed", TargetVersion: "7.3.7"}, status: ready(), wantKind: MutationErrorConflict},
		{name: "target mismatch", phase: MutationPhasePrepare, mode: model.RuntimeModeEmbedded, request: MutationRequest{RequestID: "request", TargetVersion: "7.3.6", ExpectedActiveArtifactID: testArtifactID}, state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "7.3.7"}, status: ready(), wantKind: MutationErrorConflict},
		{name: "artifact mismatch", phase: MutationPhaseActivate, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "7.3.7"}, status: mutationReadyStatus("7.3.3", testArtifactIDB, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate), wantKind: MutationErrorConflict, wantStatus: 1},
		{name: "missing trusted artifact", phase: MutationPhasePrepare, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "7.3.7"}, status: model.RuntimeObservedStatus{Identity: "runtime", Generation: 1, ProtocolVersion: "v1", State: model.RuntimeStateReady, Capabilities: model.RuntimeCapabilities{model.RuntimeCapabilityPrepareUpdate}}, wantKind: MutationErrorConflict, wantStatus: 1},
		{name: "invalid current version", phase: MutationPhasePrepare, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "7.3.7"}, status: mutationReadyStatus("7.3.7-rc.1", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate), wantKind: MutationErrorConflict, wantStatus: 1},
		{name: "prepare phase missing prepare capability", phase: MutationPhasePrepare, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "7.3.7"}, status: mutationReadyStatus("7.3.3", testArtifactID, model.RuntimeCapabilityActivateUpdate), wantKind: MutationErrorConflict, wantStatus: 1},
		{name: "prepare phase missing activate capability", phase: MutationPhasePrepare, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "7.3.7"}, status: mutationReadyStatus("7.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate), wantKind: MutationErrorConflict, wantStatus: 1},
		{name: "activate phase missing prepare capability", phase: MutationPhaseActivate, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "7.3.7"}, status: mutationReadyStatus("7.3.3", testArtifactID, model.RuntimeCapabilityActivateUpdate), wantKind: MutationErrorConflict, wantStatus: 1},
		{name: "activate phase missing activate capability", phase: MutationPhaseActivate, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "7.3.7"}, status: mutationReadyStatus("7.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate), wantKind: MutationErrorConflict, wantStatus: 1},
		{name: "non ready runtime", phase: MutationPhaseActivate, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "7.3.7"}, status: func() model.RuntimeObservedStatus {
			status := ready()
			status.State = model.RuntimeStateStarting
			return status
		}(), wantKind: MutationErrorConflict, wantStatus: 1},
		{name: "external", phase: MutationPhasePrepare, mode: model.RuntimeModeExternal, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "7.3.7"}, status: ready(), wantKind: MutationErrorConflict},
		{name: "status unavailable", phase: MutationPhaseActivate, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "7.3.7"}, statusErr: errors.New("secret transport failure"), wantKind: MutationErrorUnavailable, wantStatus: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := json.Marshal(test.state)
			if err != nil {
				t.Fatal(err)
			}
			runtimeClient := &mutationRuntime{status: test.status, statusErr: test.statusErr}
			service := New(&memoryStore{data: data}, runtimeClient, test.mode)
			source := &fakeSource{err: errors.New("must not run")}
			service.source = source
			service.now = func() time.Time { return now }
			if test.phase == MutationPhasePrepare {
				_, err = service.Prepare(t.Context(), test.request)
			} else {
				_, err = service.Activate(t.Context(), test.request)
			}
			kind, _, ok := MutationErrorDetails(err)
			if !ok || kind != test.wantKind {
				t.Fatalf("error = %v, kind = %q, want %q", err, kind, test.wantKind)
			}
			if runtimeClient.statusCalls.Load() != test.wantStatus || runtimeClient.prepareCalls.Load() != 0 || runtimeClient.activateCalls.Load() != 0 {
				t.Fatalf("calls status=%d prepare=%d activate=%d", runtimeClient.statusCalls.Load(), runtimeClient.prepareCalls.Load(), runtimeClient.activateCalls.Load())
			}
			if source.requests.Load() != 0 {
				t.Fatalf("discovery requests = %d", source.requests.Load())
			}
		})
	}
}

func TestMutationProtocolFenceErrorsAreStableAndNeverRetried(t *testing.T) {
	for _, code := range []runtimeservice.ProtocolErrorCode{
		runtimeservice.ProtocolErrorStaleRuntimeGeneration,
		runtimeservice.ProtocolErrorActiveArtifactMismatch,
		runtimeservice.ProtocolErrorRuntimeIdentityMismatch,
	} {
		t.Run(string(code), func(t *testing.T) {
			runtimeClient := &mutationRuntime{status: mutationReadyStatus("7.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate)}
			runtimeClient.prepareFn = func(model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
				return model.RuntimeOperationResult{}, &runtimeservice.ProtocolError{Code: code, HTTPStatus: 409}
			}
			service, _, _ := mutationTestService(t, runtimeClient)
			_, err := service.Prepare(t.Context(), validMutationRequest())
			kind, gotCode, ok := MutationErrorDetails(err)
			if !ok || kind != MutationErrorConflict || gotCode != string(code) {
				t.Fatalf("error details = %q, %q, %v", kind, gotCode, err)
			}
			if runtimeClient.prepareCalls.Load() != 1 {
				t.Fatalf("PrepareUpdate calls = %d", runtimeClient.prepareCalls.Load())
			}
		})
	}
}

func TestMutationTerminalFailureAndIncompleteResultsAreNotSuccess(t *testing.T) {
	tests := []struct {
		name      string
		phase     MutationPhase
		state     model.RuntimeOperationState
		failure   *model.RuntimeOperationFailure
		wantKind  MutationErrorKind
		wantCode  string
		wantState string
	}{
		{name: "prepare terminal failure", phase: MutationPhasePrepare, state: model.RuntimeOperationFailed, failure: &model.RuntimeOperationFailure{Code: "staging_failed", Message: "secret stage path"}, wantKind: MutationErrorExecution, wantCode: "staging_failed", wantState: "failed"},
		{name: "activate rollback failure", phase: MutationPhaseActivate, state: model.RuntimeOperationFailed, failure: &model.RuntimeOperationFailure{Code: "activation_readiness_failed", Message: "secret runtime URL"}, wantKind: MutationErrorExecution, wantCode: "activation_readiness_failed", wantState: "failed"},
		{name: "accepted incomplete", phase: MutationPhasePrepare, state: model.RuntimeOperationAccepted, wantKind: MutationErrorUnavailable},
		{name: "running incomplete", phase: MutationPhaseActivate, state: model.RuntimeOperationRunning, wantKind: MutationErrorUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeClient := &mutationRuntime{status: mutationReadyStatus("7.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate)}
			operation := func(request model.RuntimeMutationRequest, operationType model.RuntimeOperationType) (model.RuntimeOperationResult, error) {
				result := successfulOperation(request, operationType)
				result.State = test.state
				result.Failure = test.failure
				return result, nil
			}
			runtimeClient.prepareFn = func(request model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
				return operation(request.RuntimeMutationRequest, model.RuntimeOperationPrepareUpdate)
			}
			runtimeClient.activateFn = func(request model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
				return operation(request.RuntimeMutationRequest, model.RuntimeOperationActivateUpdate)
			}
			service, _, _ := mutationTestService(t, runtimeClient)
			var result MutationResult
			var err error
			if test.phase == MutationPhasePrepare {
				result, err = service.Prepare(t.Context(), validMutationRequest())
			} else {
				result, err = service.Activate(t.Context(), validMutationRequest())
			}
			kind, code, ok := MutationErrorDetails(err)
			if !ok || kind != test.wantKind {
				t.Fatalf("error details = %q, %q, %v", kind, code, err)
			}
			if result.State != test.wantState || result.FailureCode != test.wantCode {
				t.Fatalf("result = %#v", result)
			}
			encoded, marshalErr := json.Marshal(result)
			if marshalErr != nil || strings.Contains(string(encoded), "secret") {
				t.Fatalf("result leaked remote message: %s, %v", encoded, marshalErr)
			}
		})
	}
}

func TestMutationRejectsUnreliableOperationResult(t *testing.T) {
	runtimeClient := &mutationRuntime{status: mutationReadyStatus("7.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate)}
	runtimeClient.prepareFn = func(request model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
		result := successfulOperation(request.RuntimeMutationRequest, model.RuntimeOperationPrepareUpdate)
		result.OperationID = "wrong-operation"
		return result, nil
	}
	service, _, _ := mutationTestService(t, runtimeClient)
	_, err := service.Prepare(t.Context(), validMutationRequest())
	kind, code, ok := MutationErrorDetails(err)
	if !ok || kind != MutationErrorUnavailable || code != "runtime_result_unreliable" {
		t.Fatalf("error details = %q, %q, %v", kind, code, err)
	}
}

func TestActivateLostResponseRetryRequiresTrustedExactTarget(t *testing.T) {
	t.Run("already applied", func(t *testing.T) {
		runtimeClient := &mutationRuntime{status: mutationReadyStatus("7.3.7", testArtifactIDB, model.RuntimeCapabilityActivateUpdate)}
		service, source, _ := mutationTestService(t, runtimeClient)
		result, err := service.Activate(t.Context(), validMutationRequest())
		if err != nil || !result.AlreadyApplied || result.State != "succeeded" {
			t.Fatalf("Activate() = %#v, %v", result, err)
		}
		if runtimeClient.activateCalls.Load() != 0 || source.requests.Load() != 0 {
			t.Fatalf("activate calls = %d, discovery = %d", runtimeClient.activateCalls.Load(), source.requests.Load())
		}
	})

	for _, test := range []struct {
		name        string
		status      model.RuntimeObservedStatus
		request     MutationRequest
		storeTarget string
	}{
		{name: "different current version", status: mutationReadyStatus("7.3.8", testArtifactIDB, model.RuntimeCapabilityActivateUpdate), request: validMutationRequest(), storeTarget: "7.3.7"},
		{name: "artifact still expected", status: mutationReadyStatus("7.3.7", testArtifactID, model.RuntimeCapabilityActivateUpdate), request: validMutationRequest(), storeTarget: "7.3.7"},
		{name: "recommendation target changed", status: mutationReadyStatus("7.3.7", testArtifactIDB, model.RuntimeCapabilityActivateUpdate), request: validMutationRequest(), storeTarget: "7.3.8"},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
			runtimeClient := &mutationRuntime{status: test.status}
			service := New(persistedDiscovery(t, test.storeTarget, now), runtimeClient, model.RuntimeModeEmbedded)
			service.now = func() time.Time { return now }
			result, err := service.Activate(t.Context(), test.request)
			if err == nil || result.AlreadyApplied || runtimeClient.activateCalls.Load() != 0 {
				t.Fatalf("Activate() = %#v, %v, calls=%d", result, err, runtimeClient.activateCalls.Load())
			}
		})
	}
}

func TestMutationDoesNotHoldDiscoveryMutexAcrossRuntimeCall(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	runtimeClient := &mutationRuntime{status: mutationReadyStatus("7.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate)}
	runtimeClient.prepareFn = func(request model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
		close(entered)
		<-release
		return successfulOperation(request.RuntimeMutationRequest, model.RuntimeOperationPrepareUpdate), nil
	}
	service, _, _ := mutationTestService(t, runtimeClient)
	mutationDone := make(chan error, 1)
	go func() {
		_, err := service.Prepare(context.Background(), validMutationRequest())
		mutationDone <- err
	}()
	<-entered

	statusDone := make(chan error, 1)
	go func() {
		_, err := service.Status(context.Background())
		statusDone <- err
	}()
	select {
	case err := <-statusDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Status blocked on discovery mutex while PrepareUpdate was running")
	}
	close(release)
	if err := <-mutationDone; err != nil {
		t.Fatal(err)
	}
}

func TestMutationAdmissionRequiresBothPrepareAndActivateCapabilities(t *testing.T) {
	tests := []struct {
		name         string
		phase        MutationPhase
		capabilities []model.RuntimeCapability
	}{
		{
			name:         "prepare rejected when activate missing",
			phase:        MutationPhasePrepare,
			capabilities: []model.RuntimeCapability{model.RuntimeCapabilityPrepareUpdate},
		},
		{
			name:         "activate rejected when prepare missing",
			phase:        MutationPhaseActivate,
			capabilities: []model.RuntimeCapability{model.RuntimeCapabilityActivateUpdate},
		},
		{
			name:         "prepare rejected when prepare missing",
			phase:        MutationPhasePrepare,
			capabilities: []model.RuntimeCapability{model.RuntimeCapabilityActivateUpdate},
		},
		{
			name:         "activate rejected when activate missing",
			phase:        MutationPhaseActivate,
			capabilities: []model.RuntimeCapability{model.RuntimeCapabilityPrepareUpdate},
		},
		{
			name:         "prepare rejected when both missing",
			phase:        MutationPhasePrepare,
			capabilities: nil,
		},
		{
			name:         "activate rejected when both missing",
			phase:        MutationPhaseActivate,
			capabilities: nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeClient := &mutationRuntime{status: mutationReadyStatus("7.3.3", testArtifactID, test.capabilities...)}
			service, source, _ := mutationTestService(t, runtimeClient)
			var result MutationResult
			var err error
			if test.phase == MutationPhasePrepare {
				result, err = service.Prepare(t.Context(), validMutationRequest())
			} else {
				result, err = service.Activate(t.Context(), validMutationRequest())
			}
			kind, code, ok := MutationErrorDetails(err)
			if !ok || kind != MutationErrorConflict || code != "runtime_capability_unavailable" {
				t.Fatalf("error = %v, kind = %q, code = %q", err, kind, code)
			}
			if result != (MutationResult{}) {
				t.Fatalf("expected zero MutationResult, got %#v", result)
			}
			if runtimeClient.prepareCalls.Load() != 0 || runtimeClient.activateCalls.Load() != 0 {
				t.Fatalf("mutation calls prepare=%d activate=%d", runtimeClient.prepareCalls.Load(), runtimeClient.activateCalls.Load())
			}
			if source.requests.Load() != 0 {
				t.Fatalf("discovery requests = %d", source.requests.Load())
			}
		})
	}
}

func TestMutationProtocolErrorsNeverFabricateOperationResult(t *testing.T) {
	tests := []struct {
		name     string
		phase    MutationPhase
		code     runtimeservice.ProtocolErrorCode
		httpCode int
		wantKind MutationErrorKind
	}{
		{
			name:     "release_metadata_invalid",
			phase:    MutationPhasePrepare,
			code:     runtimeservice.ProtocolErrorReleaseMetadataInvalid,
			httpCode: 502,
			wantKind: MutationErrorExecution,
		},
		{
			name:     "unsupported_staging_platform",
			phase:    MutationPhasePrepare,
			code:     runtimeservice.ProtocolErrorUnsupportedStagingPlatform,
			httpCode: 409,
			wantKind: MutationErrorConflict,
		},
		{
			name:     "target_stage_corrupt",
			phase:    MutationPhaseActivate,
			code:     runtimeservice.ProtocolErrorTargetStageCorrupt,
			httpCode: 409,
			wantKind: MutationErrorConflict,
		},
		{
			name:     "target_stage_unavailable",
			phase:    MutationPhaseActivate,
			code:     runtimeservice.ProtocolErrorTargetStageUnavailable,
			httpCode: 409,
			wantKind: MutationErrorConflict,
		},
		{
			name:     "runtime_identity_mismatch",
			phase:    MutationPhasePrepare,
			code:     runtimeservice.ProtocolErrorRuntimeIdentityMismatch,
			httpCode: 409,
			wantKind: MutationErrorConflict,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeClient := &mutationRuntime{status: mutationReadyStatus("7.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate)}
			stubError := &runtimeservice.ProtocolError{Code: test.code, HTTPStatus: test.httpCode}
			runtimeClient.prepareFn = func(model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
				return model.RuntimeOperationResult{}, stubError
			}
			runtimeClient.activateFn = func(model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
				return model.RuntimeOperationResult{}, stubError
			}
			service, source, _ := mutationTestService(t, runtimeClient)
			var result MutationResult
			var err error
			if test.phase == MutationPhasePrepare {
				result, err = service.Prepare(t.Context(), validMutationRequest())
			} else {
				result, err = service.Activate(t.Context(), validMutationRequest())
			}
			kind, code, ok := MutationErrorDetails(err)
			if !ok || kind != test.wantKind || code != string(test.code) {
				t.Fatalf("error = %v, kind = %q, code = %q, wantKind = %q, wantCode = %q", err, kind, code, test.wantKind, test.code)
			}
			if result != (MutationResult{}) {
				t.Fatalf("fabricated operation result: %#v", result)
			}
			if result.State != "" || result.FailureCode != "" || result.RuntimeOperationID != "" || result.Phase != "" {
				t.Fatalf("result contains operation state evidence: %#v", result)
			}
			if source.requests.Load() != 0 {
				t.Fatalf("discovery requests = %d", source.requests.Load())
			}
		})
	}
}
