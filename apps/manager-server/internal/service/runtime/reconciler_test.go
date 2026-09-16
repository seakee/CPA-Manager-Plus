package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

func TestStableReconcileOperationID(t *testing.T) {
	base := StableReconcileOperationID(3, "runtime-01", 7, model.RuntimeOperationStart)
	if base != StableReconcileOperationID(3, "runtime-01", 7, model.RuntimeOperationStart) {
		t.Fatal("same reconcile tuple produced different operation IDs")
	}
	if len([]byte(base)) > 128 || !strings.HasPrefix(base, "runtime-reconcile/v1:") {
		t.Fatalf("operation ID = %q length=%d", base, len([]byte(base)))
	}
	for name, candidate := range map[string]string{
		"revision":   StableReconcileOperationID(4, "runtime-01", 7, model.RuntimeOperationStart),
		"identity":   StableReconcileOperationID(3, "runtime-02", 7, model.RuntimeOperationStart),
		"generation": StableReconcileOperationID(3, "runtime-01", 8, model.RuntimeOperationStart),
		"action":     StableReconcileOperationID(3, "runtime-01", 7, model.RuntimeOperationStop),
	} {
		if candidate == base {
			t.Fatalf("%s change did not change operation ID", name)
		}
	}
	if strings.Contains(base, "runtime-01") || strings.Contains(base, "secret-value") {
		t.Fatalf("operation ID exposes input material: %q", base)
	}
}

func TestDecideReconcileActionMatrix(t *testing.T) {
	tests := []struct {
		name     string
		desired  model.EmbeddedRuntimeDesiredLifecycle
		state    model.RuntimeState
		recovery *model.RuntimeRecoveryObservation
		want     ReconcileAction
		wantErr  bool
	}{
		{name: "running ready", desired: model.EmbeddedRuntimeDesiredRunning, state: model.RuntimeStateReady, recovery: recovery(model.RuntimeRecoveryStateArmed), want: ReconcileActionNone},
		{name: "running starting", desired: model.EmbeddedRuntimeDesiredRunning, state: model.RuntimeStateStarting, recovery: recovery(model.RuntimeRecoveryStateArmed), want: ReconcileActionNone},
		{name: "running offline inactive", desired: model.EmbeddedRuntimeDesiredRunning, state: model.RuntimeStateOffline, recovery: recovery(model.RuntimeRecoveryStateInactive), want: ReconcileActionStart},
		{name: "running offline armed", desired: model.EmbeddedRuntimeDesiredRunning, state: model.RuntimeStateOffline, recovery: recovery(model.RuntimeRecoveryStateArmed), want: ReconcileActionNone},
		{name: "running offline recovering", desired: model.EmbeddedRuntimeDesiredRunning, state: model.RuntimeStateOffline, recovery: recovery(model.RuntimeRecoveryStateRecovering), want: ReconcileActionNone},
		{name: "running manual intervention", desired: model.EmbeddedRuntimeDesiredRunning, state: model.RuntimeStateOffline, recovery: recovery(model.RuntimeRecoveryStateManualIntervention), want: ReconcileActionNone},
		{name: "stopped ready", desired: model.EmbeddedRuntimeDesiredStopped, state: model.RuntimeStateReady, recovery: recovery(model.RuntimeRecoveryStateArmed), want: ReconcileActionStop},
		{name: "stopped starting", desired: model.EmbeddedRuntimeDesiredStopped, state: model.RuntimeStateStarting, recovery: recovery(model.RuntimeRecoveryStateRecovering), want: ReconcileActionStop},
		{name: "stopped offline inactive", desired: model.EmbeddedRuntimeDesiredStopped, state: model.RuntimeStateOffline, recovery: recovery(model.RuntimeRecoveryStateInactive), want: ReconcileActionNone},
		{name: "stopped offline armed", desired: model.EmbeddedRuntimeDesiredStopped, state: model.RuntimeStateOffline, recovery: recovery(model.RuntimeRecoveryStateArmed), want: ReconcileActionNone},
		{name: "stopped offline recovering", desired: model.EmbeddedRuntimeDesiredStopped, state: model.RuntimeStateOffline, recovery: recovery(model.RuntimeRecoveryStateRecovering), want: ReconcileActionNone},
		{name: "stopped manual intervention", desired: model.EmbeddedRuntimeDesiredStopped, state: model.RuntimeStateOffline, recovery: recovery(model.RuntimeRecoveryStateManualIntervention), want: ReconcileActionNone},
		{name: "unknown fails closed", desired: model.EmbeddedRuntimeDesiredRunning, state: model.RuntimeStateUnknown, recovery: nil, want: ReconcileActionNone},
		{name: "missing recovery fails closed", desired: model.EmbeddedRuntimeDesiredRunning, state: model.RuntimeStateOffline, recovery: nil, want: ReconcileActionNone, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status := embeddedReconcileStatus(test.state, test.recovery)
			got, err := DecideReconcileAction(test.desired, status)
			if (err != nil) != test.wantErr {
				t.Fatalf("DecideReconcileAction() error = %v wantErr=%v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("DecideReconcileAction() = %q want %q", got, test.want)
			}
		})
	}
}

func TestReconcileRetriesSameOperationIdentityForFailedAndAmbiguousSubmissions(t *testing.T) {
	for _, test := range []struct {
		name       string
		startState model.RuntimeOperationState
		startErr   error
	}{
		{name: "terminal failed replay", startState: model.RuntimeOperationFailed},
		{name: "retained accepted replay", startState: model.RuntimeOperationAccepted},
		{name: "transport ambiguity", startErr: context.DeadlineExceeded},
		{name: "persistence unavailable", startErr: &ProtocolError{Code: ProtocolErrorOperationPersistenceUnavailable, HTTPStatus: 503}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &recordingRuntimeClient{status: embeddedReconcileStatus(
				model.RuntimeStateOffline,
				recovery(model.RuntimeRecoveryStateInactive),
			)}
			client.start = func(request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
				if test.startErr != nil {
					return model.RuntimeOperationResult{}, test.startErr
				}
				result := runtimeOperationResult(request, model.RuntimeOperationStart, test.startState)
				if test.startState == model.RuntimeOperationFailed {
					result.Failure = &model.RuntimeOperationFailure{Code: "process_start_failed"}
				}
				return result, nil
			}
			for range 2 {
				// Reconstruct the reconciler to model a Manager process restart.
				reconciler := NewReconciler(client, desiredStore(model.EmbeddedRuntimeDesiredRunning, 9), time.Second, nil)
				if err := reconciler.ReconcileOnce(t.Context()); err == nil {
					t.Fatal("ReconcileOnce() error = nil")
				}
			}
			requests := client.startRequests()
			if len(requests) != 2 || requests[0].OperationID != requests[1].OperationID {
				t.Fatalf("Start requests = %#v", requests)
			}
		})
	}
}

func TestReconcileFencingErrorsDoNotRetryWithinCycle(t *testing.T) {
	tests := []struct {
		code      ProtocolErrorCode
		wantError error
	}{
		{code: ProtocolErrorRuntimeIdentityMismatch, wantError: ErrReconcileReobserve},
		{code: ProtocolErrorStaleRuntimeGeneration, wantError: ErrReconcileReobserve},
		{code: ProtocolErrorOperationStateConflict, wantError: ErrReconcileReobserve},
		{code: ProtocolErrorOperationPersistenceUnavailable, wantError: ErrReconcileReobserve},
		{code: ProtocolErrorOperationIDConflict, wantError: ErrReconcileOperationConflict},
	}
	for _, test := range tests {
		t.Run(string(test.code), func(t *testing.T) {
			client := &recordingRuntimeClient{
				status: embeddedReconcileStatus(model.RuntimeStateOffline, recovery(model.RuntimeRecoveryStateInactive)),
				start: func(model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
					return model.RuntimeOperationResult{}, &ProtocolError{Code: test.code, HTTPStatus: 409}
				},
			}
			err := NewReconciler(client, desiredStore(model.EmbeddedRuntimeDesiredRunning, 1), time.Second, nil).
				ReconcileOnce(t.Context())
			if !errors.Is(err, test.wantError) {
				t.Fatalf("ReconcileOnce() error = %v want %v", err, test.wantError)
			}
			if got := len(client.startRequests()); got != 1 {
				t.Fatalf("Start calls = %d", got)
			}
		})
	}
}

func TestReconcileStatusFailureUnknownAndRecoveryOwnershipNeverMutate(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    model.RuntimeObservedStatus
		statusErr error
	}{
		{name: "status error", statusErr: errors.New("Runtime unavailable")},
		{name: "unknown", status: embeddedReconcileStatus(model.RuntimeStateUnknown, nil)},
		{name: "recovery armed", status: embeddedReconcileStatus(model.RuntimeStateOffline, recovery(model.RuntimeRecoveryStateArmed))},
		{name: "manual intervention", status: embeddedReconcileStatus(model.RuntimeStateOffline, recovery(model.RuntimeRecoveryStateManualIntervention))},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &recordingRuntimeClient{status: test.status, statusErr: test.statusErr}
			_ = NewReconciler(client, desiredStore(model.EmbeddedRuntimeDesiredRunning, 1), time.Second, nil).
				ReconcileOnce(t.Context())
			if len(client.startRequests()) != 0 || len(client.stopRequests()) != 0 {
				t.Fatalf("unexpected mutations: start=%#v stop=%#v", client.startRequests(), client.stopRequests())
			}
		})
	}
}

func TestStoppedReconcileWaitsForRecoveryReplacementThenStops(t *testing.T) {
	client := &recordingRuntimeClient{status: embeddedReconcileStatus(
		model.RuntimeStateOffline,
		recovery(model.RuntimeRecoveryStateRecovering),
	)}
	client.stop = func(request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
		return runtimeOperationResult(request, model.RuntimeOperationStop, model.RuntimeOperationSucceeded), nil
	}
	reconciler := NewReconciler(client, desiredStore(model.EmbeddedRuntimeDesiredStopped, 2), time.Second, nil)
	if err := reconciler.ReconcileOnce(t.Context()); err != nil {
		t.Fatalf("offline recovering reconcile: %v", err)
	}
	if len(client.stopRequests()) != 0 {
		t.Fatal("offline recovery attempt was raced with Stop")
	}
	client.setStatus(embeddedReconcileStatus(model.RuntimeStateStarting, recovery(model.RuntimeRecoveryStateArmed)))
	if err := reconciler.ReconcileOnce(t.Context()); err != nil {
		t.Fatalf("replacement child reconcile: %v", err)
	}
	if len(client.stopRequests()) != 1 {
		t.Fatalf("Stop calls = %#v", client.stopRequests())
	}
}

func TestCanceledReconcileWorkerNeverSubmitsStop(t *testing.T) {
	client := &recordingRuntimeClient{status: embeddedReconcileStatus(
		model.RuntimeStateReady,
		recovery(model.RuntimeRecoveryStateArmed),
	)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	NewReconciler(client, desiredStore(model.EmbeddedRuntimeDesiredStopped, 1), time.Millisecond, nil).Run(ctx)
	if len(client.stopRequests()) != 0 {
		t.Fatalf("canceled worker submitted Stop: %#v", client.stopRequests())
	}
}

func recovery(state model.RuntimeRecoveryState) *model.RuntimeRecoveryObservation {
	return &model.RuntimeRecoveryObservation{State: state, AttemptsRemaining: 2}
}

func embeddedReconcileStatus(
	state model.RuntimeState,
	recoveryObservation *model.RuntimeRecoveryObservation,
) model.RuntimeObservedStatus {
	return model.RuntimeObservedStatus{
		Identity:        "runtime-01",
		Generation:      7,
		ProtocolVersion: "v1",
		State:           state,
		Capabilities: model.RuntimeCapabilities{
			model.RuntimeCapabilityStart,
			model.RuntimeCapabilityStop,
			model.RuntimeCapabilityRestart,
		},
		Recovery: recoveryObservation,
	}
}

type staticDesiredStore struct {
	state model.EmbeddedRuntimeDesiredState
	found bool
	err   error
}

func desiredStore(lifecycle model.EmbeddedRuntimeDesiredLifecycle, revision uint64) *staticDesiredStore {
	return &staticDesiredStore{
		state: model.EmbeddedRuntimeDesiredState{
			DesiredLifecycle: lifecycle,
			Revision:         revision,
			UpdatedAtMS:      1,
		},
		found: true,
	}
}

func (s *staticDesiredStore) LoadEmbeddedRuntimeDesiredState(context.Context) (model.EmbeddedRuntimeDesiredState, bool, error) {
	return s.state, s.found, s.err
}

type recordingRuntimeClient struct {
	mu        sync.Mutex
	status    model.RuntimeObservedStatus
	statusErr error
	start     func(model.RuntimeMutationRequest) (model.RuntimeOperationResult, error)
	stop      func(model.RuntimeMutationRequest) (model.RuntimeOperationResult, error)
	starts    []model.RuntimeMutationRequest
	stops     []model.RuntimeMutationRequest
	restarts  []model.RuntimeMutationRequest
}

func (c *recordingRuntimeClient) Status(context.Context) (model.RuntimeObservedStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status, c.statusErr
}

func (c *recordingRuntimeClient) Start(_ context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	c.mu.Lock()
	c.starts = append(c.starts, request)
	callback := c.start
	c.mu.Unlock()
	if callback != nil {
		return callback(request)
	}
	return runtimeOperationResult(request, model.RuntimeOperationStart, model.RuntimeOperationSucceeded), nil
}

func (c *recordingRuntimeClient) Stop(_ context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	c.mu.Lock()
	c.stops = append(c.stops, request)
	callback := c.stop
	c.mu.Unlock()
	if callback != nil {
		return callback(request)
	}
	return runtimeOperationResult(request, model.RuntimeOperationStop, model.RuntimeOperationSucceeded), nil
}

func (c *recordingRuntimeClient) Restart(_ context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.restarts = append(c.restarts, request)
	return runtimeOperationResult(request, model.RuntimeOperationRestart, model.RuntimeOperationSucceeded), nil
}

func (c *recordingRuntimeClient) setStatus(status model.RuntimeObservedStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = status
}

func (c *recordingRuntimeClient) startRequests() []model.RuntimeMutationRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]model.RuntimeMutationRequest(nil), c.starts...)
}

func (c *recordingRuntimeClient) stopRequests() []model.RuntimeMutationRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]model.RuntimeMutationRequest(nil), c.stops...)
}

func runtimeOperationResult(
	request model.RuntimeMutationRequest,
	operationType model.RuntimeOperationType,
	state model.RuntimeOperationState,
) model.RuntimeOperationResult {
	return model.RuntimeOperationResult{
		OperationID:       request.OperationID,
		OperationType:     operationType,
		RuntimeIdentity:   request.ExpectedRuntimeIdentity,
		RuntimeGeneration: request.ExpectedRuntimeGeneration,
		State:             state,
	}
}

var _ RuntimeClient = (*recordingRuntimeClient)(nil)
