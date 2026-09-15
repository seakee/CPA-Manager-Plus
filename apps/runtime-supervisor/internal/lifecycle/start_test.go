package lifecycle

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
)

type fakeStore struct {
	mu          sync.Mutex
	steps       *[]string
	resolveOp   journal.Operation
	resolveSeen bool
	resolveErr  error
	beginOp     journal.Operation
	beginMade   bool
	beginErr    error
	beginHook   func()
	markOp      journal.Operation
	markErr     error
	completeOp  journal.Operation
	completeErr error
	beginCalls  int
}

func (store *fakeStore) ResolveSubmission(ctx context.Context, _ journal.Authority, _ journal.Intent) (journal.Operation, bool, error) {
	store.record("resolve")
	if err := ctx.Err(); err != nil {
		return journal.Operation{}, false, err
	}
	return store.resolveOp, store.resolveSeen, store.resolveErr
}

func (store *fakeStore) Begin(ctx context.Context, authority journal.Authority, intent journal.Intent) (journal.Operation, bool, error) {
	store.record("begin")
	store.mu.Lock()
	store.beginCalls++
	store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return journal.Operation{}, false, err
	}
	if store.beginHook != nil {
		store.beginHook()
	}
	if store.beginOp.OperationID == "" {
		store.beginOp = operation(intent.OperationID, authority, journal.StateAccepted)
	}
	return store.beginOp, store.beginMade, store.beginErr
}

func (store *fakeStore) MarkRunning(ctx context.Context, runtimeIdentity string, operationID string) (journal.Operation, error) {
	store.record("mark_running")
	if err := ctx.Err(); err != nil {
		return journal.Operation{}, err
	}
	if store.markOp.OperationID == "" {
		store.markOp = journal.Operation{OperationID: operationID, OperationType: startOperationType, RuntimeIdentity: runtimeIdentity, RuntimeGeneration: 41, State: journal.StateRunning}
	}
	return store.markOp, store.markErr
}

func (store *fakeStore) Complete(ctx context.Context, runtimeIdentity string, operationID string, state journal.State, failureCode string) (journal.Operation, error) {
	store.record("complete")
	if err := ctx.Err(); err != nil {
		return journal.Operation{}, err
	}
	if store.completeOp.OperationID == "" {
		store.completeOp = journal.Operation{OperationID: operationID, OperationType: startOperationType, RuntimeIdentity: runtimeIdentity, RuntimeGeneration: 41, State: state, FailureCode: failureCode}
	}
	return store.completeOp, store.completeErr
}

func (store *fakeStore) record(step string) {
	if store.steps == nil {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	*store.steps = append(*store.steps, step)
}

type fakeProcess struct {
	mu           sync.Mutex
	steps        *[]string
	observation  cpaprocess.Observation
	startErr     error
	startCalls   int
	startContexts []error
}

func (process *fakeProcess) Observe() cpaprocess.Observation {
	if process.steps != nil {
		process.mu.Lock()
		*process.steps = append(*process.steps, "observe")
		process.mu.Unlock()
	}
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.observation
}

func (process *fakeProcess) Start(ctx context.Context, _ cpaprocess.StartSpec) (cpaprocess.Observation, error) {
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.steps != nil {
		*process.steps = append(*process.steps, "start")
	}
	process.startCalls++
	process.startContexts = append(process.startContexts, ctx.Err())
	if process.startErr != nil {
		return process.observation, process.startErr
	}
	process.observation = cpaprocess.Observation{State: cpaprocess.StateRunning, PID: 1234}
	return process.observation, nil
}

func TestStartOrdersResolvePreconditionIntentBeforeSideEffect(t *testing.T) {
	steps := []string{}
	store := &fakeStore{steps: &steps, beginMade: true}
	process := &fakeProcess{steps: &steps, observation: cpaprocess.Observation{State: cpaprocess.StateNotStarted}}
	service := newService(t, store, process)

	result, err := service.Start(context.Background(), request("op-1"))
	if err != nil {
		t.Fatal(err)
	}
	if result.State != journal.StateSucceeded {
		t.Fatalf("result = %+v", result)
	}
	want := []string{"resolve", "observe", "begin", "mark_running", "start", "complete"}
	if len(steps) != len(want) {
		t.Fatalf("steps = %v, want %v", steps, want)
	}
	for index := range want {
		if steps[index] != want[index] {
			t.Fatalf("steps = %v, want %v", steps, want)
		}
	}
}

func TestStartReplayReturnsExistingWithoutPreconditionOrSpawn(t *testing.T) {
	existing := journal.Operation{OperationID: "op-replay", OperationType: startOperationType, RuntimeIdentity: "runtime-test", RuntimeGeneration: 17, State: journal.StateSucceeded}
	store := &fakeStore{resolveOp: existing, resolveSeen: true}
	process := &fakeProcess{observation: cpaprocess.Observation{State: cpaprocess.StateRunning, PID: 99}}
	service := newService(t, store, process)

	got, err := service.Start(context.Background(), request("op-replay"))
	if err != nil || got != existing {
		t.Fatalf("Start() = %+v, %v", got, err)
	}
	if process.startCalls != 0 || store.beginCalls != 0 {
		t.Fatalf("replay executed side effect: starts=%d begin=%d", process.startCalls, store.beginCalls)
	}
}

func TestStartProcessPreconditionFailsBeforeIntent(t *testing.T) {
	for _, state := range []cpaprocess.State{cpaprocess.StateRunning, cpaprocess.StateUnknown} {
		t.Run(string(state), func(t *testing.T) {
			store := &fakeStore{}
			process := &fakeProcess{observation: cpaprocess.Observation{State: state, PID: 88}}
			service := newService(t, store, process)
			_, err := service.Start(context.Background(), request("op-conflict"))
			if !errors.Is(err, ErrOperationStateConflict) {
				t.Fatalf("error = %v", err)
			}
			if store.beginCalls != 0 || process.startCalls != 0 {
				t.Fatalf("conflict wrote intent or spawned: begin=%d start=%d", store.beginCalls, process.startCalls)
			}
		})
	}
}

func TestStartPersistenceFailureBeforeSideEffectNeverSpawns(t *testing.T) {
	for name, store := range map[string]*fakeStore{
		"begin": {beginErr: errors.New("disk unavailable")},
		"mark":  {beginMade: true, markErr: errors.New("disk unavailable")},
	} {
		t.Run(name, func(t *testing.T) {
			process := &fakeProcess{observation: cpaprocess.Observation{State: cpaprocess.StateNotStarted}}
			service := newService(t, store, process)
			_, err := service.Start(context.Background(), request("op-persist"))
			if !errors.Is(err, ErrPersistenceUnavailable) {
				t.Fatalf("error = %v", err)
			}
			if process.startCalls != 0 {
				t.Fatalf("spawned %d children", process.startCalls)
			}
		})
	}
}

func TestStartSpawnFailureRecordsFailedOperation(t *testing.T) {
	store := &fakeStore{beginMade: true}
	process := &fakeProcess{observation: cpaprocess.Observation{State: cpaprocess.StateNotStarted}, startErr: cpaprocess.ErrSpawnFailed}
	service := newService(t, store, process)

	result, err := service.Start(context.Background(), request("op-spawn-failed"))
	if !errors.Is(err, ErrProcessStartFailed) {
		t.Fatalf("error = %v", err)
	}
	if result.State != journal.StateFailed || result.FailureCode != "internal_error" || process.startCalls != 1 {
		t.Fatalf("result = %+v, starts=%d", result, process.startCalls)
	}
}

func TestStartCallerCancellationAfterIntentDoesNotOwnExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &fakeStore{beginMade: true, beginHook: cancel}
	process := &fakeProcess{observation: cpaprocess.Observation{State: cpaprocess.StateNotStarted}}
	service := newService(t, store, process)

	result, err := service.Start(ctx, request("op-detached"))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if result.State != journal.StateSucceeded || process.startCalls != 1 {
		t.Fatalf("result = %+v, starts=%d", result, process.startCalls)
	}
	if len(process.startContexts) != 1 || process.startContexts[0] != nil {
		t.Fatalf("process execution inherited caller cancellation: %v", process.startContexts)
	}
}

func TestStartResultPersistenceFailureDoesNotReplaySideEffect(t *testing.T) {
	store := &fakeStore{beginMade: true, completeErr: errors.New("disk unavailable")}
	process := &fakeProcess{observation: cpaprocess.Observation{State: cpaprocess.StateNotStarted}}
	service := newService(t, store, process)

	running, err := service.Start(context.Background(), request("op-result-failure"))
	if !errors.Is(err, ErrResultPersistenceAfterStart) || process.startCalls != 1 {
		t.Fatalf("first Start() = %+v, %v, starts=%d", running, err, process.startCalls)
	}
	store.resolveOp = running
	store.resolveSeen = true
	store.completeErr = nil
	got, err := service.Start(context.Background(), request("op-result-failure"))
	if err != nil || got != running || process.startCalls != 1 {
		t.Fatalf("replay Start() = %+v, %v, starts=%d", got, err, process.startCalls)
	}
}

func TestStartSerializesDifferentOperationIDs(t *testing.T) {
	store := &fakeStore{beginMade: true}
	process := &fakeProcess{observation: cpaprocess.Observation{State: cpaprocess.StateNotStarted}}
	service := newService(t, store, process)

	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range []string{"op-a", "op-b"} {
		id := id
		go func() {
			<-start
			_, err := service.Start(context.Background(), request(id))
			results <- err
		}()
	}
	close(start)
	first := <-results
	second := <-results
	if first != nil && second != nil {
		t.Fatalf("both Start calls failed: %v / %v", first, second)
	}
	if first == nil && second == nil {
		t.Fatal("both Start calls succeeded")
	}
	if !errors.Is(first, ErrOperationStateConflict) && !errors.Is(second, ErrOperationStateConflict) {
		t.Fatalf("missing state conflict: %v / %v", first, second)
	}
	if process.startCalls != 1 {
		t.Fatalf("spawned %d children", process.startCalls)
	}
}

func newService(t *testing.T, store journalStore, process processManager) *StartService {
	t.Helper()
	service, err := NewStartService(store, journal.Authority{RuntimeIdentity: "runtime-test", RuntimeGeneration: 41}, process, cpaprocess.StartSpec{Executable: "cpa-test"})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func request(operationID string) StartRequest {
	return StartRequest{OperationID: operationID, ExpectedRuntimeIdentity: "runtime-test", ExpectedRuntimeGeneration: 41}
}

func operation(operationID string, authority journal.Authority, state journal.State) journal.Operation {
	return journal.Operation{OperationID: operationID, OperationType: startOperationType, RuntimeIdentity: authority.RuntimeIdentity, RuntimeGeneration: authority.RuntimeGeneration, State: state}
}
