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
	observeFn  func(model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error)
	observeErr error

	recordedGenerations map[string]model.RuntimeGeneration

	statusCalls   atomic.Int32
	prepareCalls  atomic.Int32
	activateCalls atomic.Int32
	observeCalls  atomic.Int32

	lastPrepare  model.RuntimePrepareUpdateRequest
	lastActivate model.RuntimeActivateUpdateRequest
	lastObserve  model.RuntimeObserveUpdateOperationRequest
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
	var (
		op  model.RuntimeOperationResult
		err error
	)
	if fn != nil {
		op, err = fn(request)
	} else {
		op = successfulOperation(request.RuntimeMutationRequest, model.RuntimeOperationActivateUpdate)
	}
	if err == nil && op.State == model.RuntimeOperationSucceeded && op.RuntimeGeneration != 0 {
		r.mu.Lock()
		if r.recordedGenerations == nil {
			r.recordedGenerations = make(map[string]model.RuntimeGeneration)
		}
		r.recordedGenerations[op.OperationID] = op.RuntimeGeneration
		r.mu.Unlock()
	}
	return op, err
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

func (r *mutationRuntime) ObserveUpdateOperation(_ context.Context, request model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
	r.observeCalls.Add(1)
	r.mu.Lock()
	r.lastObserve = request
	fn := r.observeFn
	err := r.observeErr
	gen := request.ExpectedRuntimeGeneration
	if recorded, ok := r.recordedGenerations[request.OperationID]; ok && recorded != 0 {
		gen = recorded
	}
	r.mu.Unlock()
	if fn != nil {
		return fn(request)
	}
	if err != nil {
		return model.RuntimeUpdateOperationObservation{}, err
	}
	created := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	completed := created.Add(time.Second)
	return model.RuntimeUpdateOperationObservation{
		OperationID:       request.OperationID,
		OperationType:     request.OperationType,
		RuntimeIdentity:   request.ExpectedRuntimeIdentity,
		RuntimeGeneration: gen,
		State:             model.RuntimeOperationSucceeded,
		CreatedAt:         created,
		UpdatedAt:         completed,
		CompletedAt:       &completed,
	}, nil
}

func completedInstallProbe(context.Context) (bool, error) { return true, nil }

func mutationTestService(t *testing.T, runtimeClient *mutationRuntime) (*Service, *fakeSource, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	source := &fakeSource{err: errors.New("mutation must not perform discovery")}
	service := New(persistedDiscovery(t, "8.3.7", now), runtimeClient, model.RuntimeModeEmbedded).WithProductCompleteProbe(completedInstallProbe)
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
		TargetVersion:            "8.3.7",
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
				"8.3.3",
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
			wantStatusCalls := int32(1)
			if phase == MutationPhaseActivate {
				wantStatusCalls = 2
			}
			if source.requests.Load() != 0 || runtimeClient.statusCalls.Load() != wantStatusCalls {
				t.Fatalf("source calls = %d, status calls = %d, want %d", source.requests.Load(), runtimeClient.statusCalls.Load(), wantStatusCalls)
			}
			if phase == MutationPhasePrepare {
				if runtimeClient.prepareCalls.Load() != 1 || runtimeClient.activateCalls.Load() != 0 {
					t.Fatalf("prepare calls = %d, activate calls = %d", runtimeClient.prepareCalls.Load(), runtimeClient.activateCalls.Load())
				}
				got := runtimeClient.lastPrepare
				if got.ExpectedRuntimeIdentity != "runtime-mutation-test" || got.ExpectedRuntimeGeneration != 42 ||
					got.ExpectedActiveArtifactID != testArtifactID || got.TargetVersion != "8.3.7" ||
					got.OperationID != runtimeOperationID(phase, request.RequestID) {
					t.Fatalf("PrepareUpdate request = %#v", got)
				}
			} else {
				if runtimeClient.activateCalls.Load() != 1 || runtimeClient.prepareCalls.Load() != 0 {
					t.Fatalf("activate calls = %d, prepare calls = %d", runtimeClient.activateCalls.Load(), runtimeClient.prepareCalls.Load())
				}
				got := runtimeClient.lastActivate
				if got.ExpectedRuntimeIdentity != "runtime-mutation-test" || got.ExpectedRuntimeGeneration != 42 ||
					got.ExpectedActiveArtifactID != testArtifactID || got.TargetVersion != "8.3.7" ||
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
	if prepare != newMutationResult(MutationPhasePrepare, MutationRequest{RequestID: requestID, TargetVersion: "8.3.7"}).RuntimeOperationID ||
		prepare != newMutationResult(MutationPhasePrepare, MutationRequest{RequestID: requestID, TargetVersion: "8.3.8"}).RuntimeOperationID {
		t.Fatal("target version changed the operation ID")
	}
	if strings.Contains(prepare, requestID) || !strings.HasPrefix(prepare, "manager-cpa-update/v1:prepare:") || len(strings.TrimPrefix(prepare, "manager-cpa-update/v1:prepare:")) != 64 {
		t.Fatalf("operation ID shape = %q", prepare)
	}
	request := validMutationRequest()
	request.RequestID = requestID
	firstRuntime := &mutationRuntime{status: mutationReadyStatus("8.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate)}
	firstService, _, now := mutationTestService(t, firstRuntime)
	firstResult, err := firstService.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	retryResult, err := firstService.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	secondRuntime := &mutationRuntime{status: mutationReadyStatus("8.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate)}
	secondService, _, _ := mutationTestService(t, secondRuntime)
	restartedResult, err := secondService.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	changedRuntime := &mutationRuntime{status: mutationReadyStatus("8.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate)}
	changedService := New(persistedDiscovery(t, "8.3.8", now), changedRuntime, model.RuntimeModeEmbedded).WithProductCompleteProbe(completedInstallProbe)
	changedService.now = func() time.Time { return now }
	changedRequest := request
	changedRequest.TargetVersion = "8.3.8"
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
		return mutationReadyStatus("8.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate)
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
		{name: "stale recommendation", phase: MutationPhasePrepare, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now.Add(-discoveryFreshness - time.Second), LastSuccessAt: now.Add(-discoveryFreshness - time.Second), TargetVersion: "8.3.7"}, status: ready(), wantKind: MutationErrorConflict},
		{name: "discovery error", phase: MutationPhaseActivate, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now.Add(-time.Minute), LastError: "failed", TargetVersion: "8.3.7"}, status: ready(), wantKind: MutationErrorConflict},
		{name: "target mismatch", phase: MutationPhasePrepare, mode: model.RuntimeModeEmbedded, request: MutationRequest{RequestID: "request", TargetVersion: "8.3.6", ExpectedActiveArtifactID: testArtifactID}, state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "8.3.7"}, status: ready(), wantKind: MutationErrorConflict},
		{name: "artifact mismatch", phase: MutationPhaseActivate, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "8.3.7"}, status: mutationReadyStatus("8.3.3", testArtifactIDB, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate), wantKind: MutationErrorConflict, wantStatus: 1},
		{name: "missing trusted artifact", phase: MutationPhasePrepare, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "8.3.7"}, status: model.RuntimeObservedStatus{Identity: "runtime", Generation: 1, ProtocolVersion: "v1", State: model.RuntimeStateReady, Capabilities: model.RuntimeCapabilities{model.RuntimeCapabilityPrepareUpdate}}, wantKind: MutationErrorConflict, wantStatus: 1},
		{name: "invalid current version", phase: MutationPhasePrepare, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "8.3.7"}, status: mutationReadyStatus("8.3.7-rc.1", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate), wantKind: MutationErrorConflict, wantStatus: 1},
		{name: "prepare phase missing prepare capability", phase: MutationPhasePrepare, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "8.3.7"}, status: mutationReadyStatus("8.3.3", testArtifactID, model.RuntimeCapabilityActivateUpdate), wantKind: MutationErrorConflict, wantStatus: 1},
		{name: "prepare phase missing activate capability", phase: MutationPhasePrepare, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "8.3.7"}, status: mutationReadyStatus("8.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate), wantKind: MutationErrorConflict, wantStatus: 1},
		{name: "activate phase missing prepare capability", phase: MutationPhaseActivate, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "8.3.7"}, status: mutationReadyStatus("8.3.3", testArtifactID, model.RuntimeCapabilityActivateUpdate), wantKind: MutationErrorConflict, wantStatus: 1},
		{name: "activate phase missing activate capability", phase: MutationPhaseActivate, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "8.3.7"}, status: mutationReadyStatus("8.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate), wantKind: MutationErrorConflict, wantStatus: 1},
		{name: "non ready runtime", phase: MutationPhaseActivate, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "8.3.7"}, status: func() model.RuntimeObservedStatus {
			status := ready()
			status.State = model.RuntimeStateStarting
			return status
		}(), wantKind: MutationErrorConflict, wantStatus: 1},
		{name: "external", phase: MutationPhasePrepare, mode: model.RuntimeModeExternal, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "8.3.7"}, status: ready(), wantKind: MutationErrorConflict},
		{name: "status unavailable", phase: MutationPhaseActivate, mode: model.RuntimeModeEmbedded, request: validMutationRequest(), state: DiscoveryState{SchemaVersion: 1, LastAttemptAt: now, LastSuccessAt: now, TargetVersion: "8.3.7"}, statusErr: errors.New("secret transport failure"), wantKind: MutationErrorUnavailable, wantStatus: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := json.Marshal(test.state)
			if err != nil {
				t.Fatal(err)
			}
			runtimeClient := &mutationRuntime{status: test.status, statusErr: test.statusErr}
			service := New(&memoryStore{data: data}, runtimeClient, test.mode).WithProductCompleteProbe(completedInstallProbe)
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
			runtimeClient := &mutationRuntime{status: mutationReadyStatus("8.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate)}
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
			runtimeClient := &mutationRuntime{status: mutationReadyStatus("8.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate)}
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
	runtimeClient := &mutationRuntime{status: mutationReadyStatus("8.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate)}
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
		runtimeClient := &mutationRuntime{status: mutationReadyStatus("8.3.7", testArtifactIDB, model.RuntimeCapabilityActivateUpdate)}
		runtimeClient.activateFn = func(req model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
			if req.ExpectedActiveArtifactID != testArtifactID || req.OperationID != runtimeOperationID(MutationPhaseActivate, validMutationRequest().RequestID) {
				t.Fatalf("replay discarded original operation fence: %#v", req)
			}
			return successfulOperation(req.RuntimeMutationRequest, model.RuntimeOperationActivateUpdate), nil
		}
		service, source, _ := mutationTestService(t, runtimeClient)
		result, err := service.Activate(t.Context(), validMutationRequest())
		if err != nil || !result.AlreadyApplied || result.State != "succeeded" {
			t.Fatalf("Activate() = %#v, %v", result, err)
		}
		if runtimeClient.activateCalls.Load() != 1 || source.requests.Load() != 0 {
			t.Fatalf("activate calls = %d, discovery = %d", runtimeClient.activateCalls.Load(), source.requests.Load())
		}
	})
	t.Run("same version without exact digest evidence is not applied", func(t *testing.T) {
		client := &mutationRuntime{status: mutationReadyStatus("8.3.7", testArtifactIDB, model.RuntimeCapabilityActivateUpdate)}
		client.activateFn = func(model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
			return model.RuntimeOperationResult{}, &runtimeservice.ProtocolError{HTTPStatus: 409, Code: runtimeservice.ProtocolErrorActiveArtifactMismatch}
		}
		service, _, _ := mutationTestService(t, client)
		got, err := service.Activate(t.Context(), validMutationRequest())
		if kind, _, _ := MutationErrorDetails(err); kind != MutationErrorConflict || got.AlreadyApplied {
			t.Fatalf("Activate() = %#v, %v", got, err)
		}
	})

	for _, test := range []struct {
		name        string
		status      model.RuntimeObservedStatus
		request     MutationRequest
		storeTarget string
	}{
		{name: "different current version", status: mutationReadyStatus("8.3.8", testArtifactIDB, model.RuntimeCapabilityActivateUpdate), request: validMutationRequest(), storeTarget: "8.3.7"},
		{name: "artifact still expected", status: mutationReadyStatus("8.3.7", testArtifactID, model.RuntimeCapabilityActivateUpdate), request: validMutationRequest(), storeTarget: "8.3.7"},
		{name: "recommendation target changed", status: mutationReadyStatus("8.3.7", testArtifactIDB, model.RuntimeCapabilityActivateUpdate), request: validMutationRequest(), storeTarget: "8.3.8"},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
			runtimeClient := &mutationRuntime{status: test.status}
			service := New(persistedDiscovery(t, test.storeTarget, now), runtimeClient, model.RuntimeModeEmbedded).WithProductCompleteProbe(completedInstallProbe)
			service.now = func() time.Time { return now }
			result, err := service.Activate(t.Context(), test.request)
			if err == nil || result.AlreadyApplied || runtimeClient.activateCalls.Load() != 0 {
				t.Fatalf("Activate() = %#v, %v, calls=%d", result, err, runtimeClient.activateCalls.Load())
			}
		})
	}
}

func TestMutationsRequireCompleteProductAndV8(t *testing.T) {
	client := &mutationRuntime{status: mutationReadyStatus("8.3.3", testArtifactID)}
	service, _, _ := mutationTestService(t, client)
	service.WithProductCompleteProbe(func(context.Context) (bool, error) { return false, nil })
	for _, call := range []func(context.Context, MutationRequest) (MutationResult, error){
		service.Prepare, service.Activate,
		func(ctx context.Context, req MutationRequest) (MutationResult, error) {
			return service.PrepareUpload(ctx, req, strings.NewReader("archive"), 7)
		},
	} {
		req := validMutationRequest()
		if _, err := call(t.Context(), req); err == nil {
			t.Fatal("mutation admitted before ProductComplete")
		} else if kind, code, _ := MutationErrorDetails(err); kind != MutationErrorConflict || code != "product_incomplete" {
			t.Fatalf("incomplete product: %q %q", kind, code)
		}
		req.TargetVersion = "9.0.0"
		if _, err := call(t.Context(), req); err == nil {
			t.Fatal("mutation admitted unsupported major")
		} else if kind, code, _ := MutationErrorDetails(err); kind != MutationErrorInvalid || code != "unsupported_cpa_major" {
			t.Fatalf("unsupported major: %q %q", kind, code)
		}
	}
	if client.statusCalls.Load() != 0 || client.prepareCalls.Load() != 0 || client.activateCalls.Load() != 0 {
		t.Fatal("mutation reached the Supervisor before admission")
	}
}

func TestMutationValidateAndExecuteEnforce8055MinimumBoundary(t *testing.T) {
	base := validMutationRequest()
	for _, rejected := range []struct {
		version  string
		wantCode string
	}{
		{version: "8.0.54", wantCode: "unsupported_cpa_major"},
		{version: "v8.0.54", wantCode: "invalid_request"},
	} {
		req := base
		req.TargetVersion = rejected.version
		err := req.Validate()
		kind, code, ok := MutationErrorDetails(err)
		if !ok || kind != MutationErrorInvalid || code != rejected.wantCode {
			t.Fatalf("Validate(%q) = (%v, %q, %q), want invalid/%s", rejected.version, err, kind, code, rejected.wantCode)
		}
	}
	for _, accepted := range []struct {
		current string
		target  string
	}{
		{current: "8.0.54", target: "8.0.55"},
		{current: "8.0.55", target: "8.0.56"},
	} {
		req := base
		req.TargetVersion = accepted.target
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate(%q) error = %v", accepted.target, err)
		}
		now := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
		runtimeClient := &mutationRuntime{status: mutationReadyStatus(
			accepted.current,
			testArtifactID,
			model.RuntimeCapabilityPrepareUpdate,
			model.RuntimeCapabilityActivateUpdate,
		)}
		service := New(persistedDiscovery(t, accepted.target, now), runtimeClient, model.RuntimeModeEmbedded).WithProductCompleteProbe(completedInstallProbe)
		service.now = func() time.Time { return now }
		if _, err := service.Prepare(t.Context(), req); err != nil {
			t.Fatalf("Prepare(%q) error = %v", accepted.target, err)
		}
		if _, err := service.Activate(t.Context(), req); err != nil {
			t.Fatalf("Activate(%q) error = %v", accepted.target, err)
		}
	}
}

func TestConcurrentActivateSerializesAcrossPostStatusAndReceiptRecording(t *testing.T) {
	allCaps := []model.RuntimeCapability{
		model.RuntimeCapabilityPrepareUpdate,
		model.RuntimeCapabilityActivateUpdate,
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	runtimeClient := &mutationRuntime{status: mutationReadyStatus("8.3.3", testArtifactID, allCaps...)}
	runtimeClient.activateFn = func(req model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
		close(entered)
		<-release
		runtimeClient.mu.Lock()
		runtimeClient.status = mutationReadyStatus("8.3.7", testArtifactIDB, allCaps...)
		runtimeClient.mu.Unlock()
		return successfulOperation(req.RuntimeMutationRequest, model.RuntimeOperationActivateUpdate), nil
	}
	service, _, _ := mutationTestService(t, runtimeClient)
	req := validMutationRequest()

	type outcome struct {
		result MutationResult
		err    error
	}
	firstDone := make(chan outcome, 1)
	go func() {
		res, err := service.Activate(context.Background(), req)
		firstDone <- outcome{result: res, err: err}
	}()
	<-entered

	secondStarted := make(chan struct{})
	secondDone := make(chan outcome, 1)
	go func() {
		close(secondStarted)
		res, err := service.Activate(context.Background(), req)
		secondDone <- outcome{result: res, err: err}
	}()
	<-secondStarted

	// Read-only Status must remain non-blocking while mutationMu is held.
	if _, err := service.Status(context.Background()); err != nil {
		t.Fatalf("Status() while Activate running failed: %v", err)
	}
	// Second Activate must wait on mutationMu without issuing ActivateUpdate.
	select {
	case early := <-secondDone:
		t.Fatalf("concurrent Activate did not wait on serialization: %#v, %v", early.result, early.err)
	case <-time.After(25 * time.Millisecond):
	}
	if runtimeClient.activateCalls.Load() != 1 {
		t.Fatalf("activateCalls while serialized = %d, want 1", runtimeClient.activateCalls.Load())
	}

	close(release)
	first := <-firstDone
	second := <-secondDone
	if first.err != nil || first.result.AlreadyApplied || first.result.State != string(model.RuntimeOperationSucceeded) {
		t.Fatalf("first Activate = %#v, %v", first.result, first.err)
	}
	if second.err != nil || !second.result.AlreadyApplied || second.result.State != string(model.RuntimeOperationSucceeded) {
		t.Fatalf("second Activate = %#v, %v", second.result, second.err)
	}
	if runtimeClient.activateCalls.Load() != 1 {
		t.Fatalf("total activateCalls = %d, want 1", runtimeClient.activateCalls.Load())
	}
}

func TestMutationDoesNotHoldDiscoveryMutexAcrossRuntimeCall(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	runtimeClient := &mutationRuntime{status: mutationReadyStatus("8.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate)}
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
			runtimeClient := &mutationRuntime{status: mutationReadyStatus("8.3.3", testArtifactID, test.capabilities...)}
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
			runtimeClient := &mutationRuntime{status: mutationReadyStatus("8.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate)}
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

func TestPrepareForwardsValidatedCDNPrefixAndRejectsCDNOnActivate(t *testing.T) {
	var captured model.RuntimePrepareUpdateRequest
	runtimeClient := &mutationRuntime{status: mutationReadyStatus("8.3.3", testArtifactID, model.RuntimeCapabilityPrepareUpdate, model.RuntimeCapabilityActivateUpdate)}
	runtimeClient.prepareFn = func(req model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
		captured = req
		return model.RuntimeOperationResult{
			OperationID: req.OperationID, OperationType: model.RuntimeOperationPrepareUpdate,
			RuntimeIdentity: req.ExpectedRuntimeIdentity, RuntimeGeneration: req.ExpectedRuntimeGeneration,
			State: model.RuntimeOperationSucceeded,
		}, nil
	}
	service, _, _ := mutationTestService(t, runtimeClient)
	req := validMutationRequest()
	req.SourceMode = "cdn"
	req.CDNPrefix = "https://cdn.example.com/proxy/"
	if _, err := service.Prepare(t.Context(), req); err != nil {
		t.Fatalf("Prepare with valid CDN failed: %v", err)
	}
	if captured.SourceMode != "cdn" || captured.CDNPrefix != "https://cdn.example.com/proxy/" {
		t.Fatalf("captured = %+v", captured)
	}
	bad := validMutationRequest()
	bad.SourceMode = "cdn"
	bad.CDNPrefix = "http://cdn.example.com/"
	if _, err := service.Prepare(t.Context(), bad); err == nil {
		t.Fatal("expected HTTP CDN prefix to be rejected")
	}
	if _, err := service.Activate(t.Context(), req); err == nil {
		t.Fatal("expected CDN fields on Activate to be rejected")
	}
}
