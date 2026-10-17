package lifecycle

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/selection"
)

func TestFreshStartRejectsSameGenerationArtifactChangeInsideLifecycleLock(t *testing.T) {
	f := newPrepareFixture(t, activeArtifactA)
	request := startRequest("fresh-start")
	request.ExpectedActiveArtifactID = activeArtifactA
	// Manager has already observed/staged A. An activation holds the same
	// lifecycle gate and selects B before Fresh's submitted Start acquires it.
	f.starter.mu.Lock()
	done := make(chan error, 1)
	go func() { _, err := f.starter.Start(t.Context(), request); done <- err }()
	f.starter.active = selection.Bundled("supervisor-local-b", "")
	f.observer.observation.ArtifactID = activeArtifactB
	f.starter.mu.Unlock()
	if err := <-done; !errors.Is(err, ErrActiveArtifactMismatch) {
		t.Fatalf("same-generation A -> B escaped fencing: %v", err)
	}
	if f.child.starts != 0 || f.starter.authority.RuntimeGeneration != request.ExpectedRuntimeGeneration {
		t.Fatal("artifact mismatch changed process/generation")
	}
	if _, err := f.journal.Store.Get(t.Context(), "runtime-01", request.OperationID); !errors.Is(err, journal.ErrOperationNotFound) {
		t.Fatalf("artifact mismatch persisted an intent: %v", err)
	}
}

func TestFreshStartArtifactFingerprintFencesReplayAndPreservesOriginalResult(t *testing.T) {
	f := newPrepareFixture(t, activeArtifactA)
	request := startRequest("fresh-start")
	request.ExpectedActiveArtifactID = activeArtifactA
	first, err := f.starter.Start(t.Context(), request)
	if err != nil || first.State != journal.StateSucceeded || first.RequestFingerprint != sha256.Sum256([]byte(`runtime.start/v1:{"expectedActiveArtifactId":"`+string(activeArtifactA)+`"}`)) {
		t.Fatalf("first artifact-bound Start: %+v %v", first, err)
	}
	// A lost response must return A's retained result without starting the
	// newly selected B, and the same operation cannot be rebound or downgraded.
	f.observer.observation.ArtifactID = activeArtifactB
	f.events = nil
	replay, err := f.starter.Start(t.Context(), request)
	if err != nil || !reflect.DeepEqual(replay, first) || f.child.starts != 1 || !reflect.DeepEqual(f.events, []string{"resolve"}) {
		t.Fatalf("artifact-bound replay: %+v %v events=%v", replay, err, f.events)
	}
	for _, newArtifact := range []artifact.ID{activeArtifactB, ""} {
		request.ExpectedActiveArtifactID = newArtifact
		if _, err := f.starter.Start(t.Context(), request); !errors.Is(err, journal.ErrOperationIDConflict) || f.child.starts != 1 {
			t.Fatalf("rebound original operation: %v", err)
		}
	}
}

var injectedFailure = errors.New("injected storage failure")

func TestStartOrdersDurableEvidenceBeforeSpawn(t *testing.T) {
	f := newStartFixture(t)
	f.child.onStart = func(ctx context.Context, spec cpaprocess.StartSpec) error {
		if spec.Executable != "supervisor-local-cpa" || len(spec.Args) != 0 {
			t.Fatalf("execution input = %+v", spec)
		}
		// A separate connection sees committed evidence before Start runs.
		reader, err := journal.Open(ctx, f.path, journal.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		operation, err := reader.Get(ctx, "runtime-01", "op")
		if err != nil || operation.State != journal.StateRunning || operation.RuntimeGeneration != 41 ||
			operation.RequestFingerprint != sha256.Sum256([]byte("runtime.start/v1:{}")) {
			t.Fatalf("pre-spawn durable evidence = %+v, %v", operation, err)
		}
		return nil
	}
	result, err := f.starter.Start(t.Context(), startRequest("op"))
	if err != nil || result.State != journal.StateSucceeded {
		t.Fatalf("Start = %+v, %v", result, err)
	}
	want := []string{"resolve", "observe", "begin", "running", "spawn", "complete:succeeded"}
	if !reflect.DeepEqual(f.events, want) {
		t.Fatalf("ordering = %v, want %v", f.events, want)
	}
	if f.starter.authority.RuntimeGeneration != 41 {
		t.Fatal("Start changed Supervisor generation")
	}
}

func TestStartFailureWindowsNeverResumeOnReplay(t *testing.T) {
	tests := []struct {
		name       string
		failAt     string
		spawnError bool
		wantState  journal.State
		wantStarts int
		wantError  error
	}{
		{"lookup", "resolve", false, "", 0, ErrPersistenceUnavailable},
		{"intent", "begin", false, "", 0, ErrPersistenceUnavailable},
		{"running", "running", false, journal.StateAccepted, 0, ErrPersistenceUnavailable},
		{"spawn", "", true, journal.StateFailed, 1, ErrExecutionFailed},
		{"success result", "complete", false, journal.StateRunning, 1, ErrExecutionFailed},
		{"failure result", "complete", true, journal.StateRunning, 1, ErrExecutionFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newStartFixture(t)
			f.journal.failAt = test.failAt
			if test.spawnError {
				f.child.onStart = func(context.Context, cpaprocess.StartSpec) error { return cpaprocess.ErrSpawnFailed }
			}
			_, err := f.starter.Start(t.Context(), startRequest("op"))
			if !errors.Is(err, test.wantError) || f.child.starts != test.wantStarts {
				t.Fatalf("Start error %v, spawns %d", err, f.child.starts)
			}
			stored, err := f.journal.Store.Get(t.Context(), "runtime-01", "op")
			if test.wantState == "" {
				if !errors.Is(err, journal.ErrOperationNotFound) {
					t.Fatalf("unexpected intent: %+v, %v", stored, err)
				}
				return
			}
			if err != nil || stored.State != test.wantState {
				t.Fatalf("retained state = %+v, %v", stored, err)
			}
			if test.wantState == journal.StateFailed && stored.FailureCode != "process_start_failed" {
				t.Fatalf("failure evidence = %+v", stored)
			}
			if test.spawnError && f.child.observation.State != cpaprocess.StateNotStarted {
				t.Fatalf("failed spawn invented a running child: %+v", f.child.observation)
			}
			f.journal.failAt = ""
			f.events = nil
			replay, err := f.starter.Start(t.Context(), startRequest("op"))
			if err != nil || !reflect.DeepEqual(replay, stored) || f.child.starts != test.wantStarts ||
				!reflect.DeepEqual(f.events, []string{"resolve"}) {
				t.Fatalf("replay = %+v, %v; starts %d; events %v", replay, err, f.child.starts, f.events)
			}
		})
	}
}

func TestStartPreconditionRejectsOwnedOrUnknownChildWithoutIntent(t *testing.T) {
	for _, state := range []cpaprocess.State{cpaprocess.StateRunning, cpaprocess.StateUnknown, "unrecognized"} {
		t.Run(string(state), func(t *testing.T) {
			f := newStartFixture(t)
			f.child.observation = cpaprocess.Observation{State: state, PID: 123}
			_, err := f.starter.Start(t.Context(), startRequest("op"))
			if !errors.Is(err, journal.ErrOperationStateConflict) || f.child.starts != 0 ||
				!reflect.DeepEqual(f.events, []string{"resolve", "observe"}) {
				t.Fatalf("Start error %v; starts %d; events %v", err, f.child.starts, f.events)
			}
			if _, err := f.journal.Store.Get(t.Context(), "runtime-01", "op"); !errors.Is(err, journal.ErrOperationNotFound) {
				t.Fatalf("precondition failure wrote intent: %v", err)
			}
		})
	}
}

func TestStartFencingPrecedesProcessPrecondition(t *testing.T) {
	for _, identityMismatch := range []bool{true, false} {
		f := newStartFixture(t)
		f.child.observation.State = cpaprocess.StateRunning
		request := startRequest("op")
		request.ExpectedRuntimeGeneration++
		want := journal.ErrStaleRuntimeGeneration
		if identityMismatch {
			request.ExpectedRuntimeIdentity = "another-runtime"
			want = journal.ErrRuntimeIdentityMismatch
		}
		_, err := f.starter.Start(t.Context(), request)
		if !errors.Is(err, want) || !reflect.DeepEqual(f.events, []string{"resolve"}) {
			t.Fatalf("fenced Start error = %v, events %v", err, f.events)
		}
	}
}

func TestStartReplaysAllRetainedStatesWithCreationGeneration(t *testing.T) {
	for _, state := range []journal.State{journal.StateAccepted, journal.StateRunning, journal.StateSucceeded, journal.StateFailed} {
		t.Run(string(state), func(t *testing.T) {
			f := newStartFixture(t)
			intent := startIntent("op")
			if _, _, err := f.journal.Store.Begin(t.Context(), f.starter.authority, intent); err != nil {
				t.Fatal(err)
			}
			var err error
			if state == journal.StateRunning {
				_, err = f.journal.Store.MarkRunning(t.Context(), "runtime-01", "op")
			} else if state != journal.StateAccepted {
				code := ""
				if state == journal.StateFailed {
					code = "process_start_failed"
				}
				_, err = f.journal.Store.Complete(t.Context(), "runtime-01", "op", state, code)
			}
			if err != nil {
				t.Fatal(err)
			}
			f.child.observation.State = cpaprocess.StateUnknown
			f.starter.authority.RuntimeGeneration = 42
			request := startRequest("op")
			request.ExpectedRuntimeGeneration = 42
			got, err := f.starter.Start(t.Context(), request)
			if err != nil || got.State != state || got.RuntimeGeneration != 41 || f.child.starts != 0 ||
				!reflect.DeepEqual(f.events, []string{"resolve"}) {
				t.Fatalf("retained replay = %+v, %v; events %v", got, err, f.events)
			}
		})
	}
}

func TestStartHonorsDuplicateDiscoveredByBegin(t *testing.T) {
	f := newStartFixture(t)
	f.journal.beforeBegin = func() {
		if _, _, err := f.journal.Store.Begin(t.Context(), f.starter.authority, startIntent("op")); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.starter.Start(t.Context(), startRequest("op"))
	if err != nil || got.State != journal.StateAccepted || f.child.starts != 0 ||
		!reflect.DeepEqual(f.events, []string{"resolve", "observe", "begin"}) {
		t.Fatalf("Begin replay = %+v, %v; events %v", got, err, f.events)
	}
}

func TestStartCallerCancellationBeforeAndAfterDurableIntent(t *testing.T) {
	for _, when := range []string{"before submission", "before commit", "after commit"} {
		t.Run(when, func(t *testing.T) {
			f := newStartFixture(t)
			type requestKey struct{}
			ctx, cancel := context.WithCancel(context.WithValue(t.Context(), requestKey{}, "caller-value"))
			defer cancel()
			switch when {
			case "before submission":
				cancel()
			case "before commit":
				f.journal.beforeBegin = cancel
			case "after commit":
				f.journal.afterBegin = cancel
			}
			f.child.onStart = func(execution context.Context, _ cpaprocess.StartSpec) error {
				if ctx.Err() == nil || execution.Err() != nil || execution.Value(requestKey{}) != nil {
					t.Fatal("accepted execution still belongs to caller context")
				}
				return nil
			}
			got, err := f.starter.Start(ctx, startRequest("op"))
			if when == "after commit" {
				if err != nil || got.State != journal.StateSucceeded || f.child.starts != 1 {
					t.Fatalf("accepted Start = %+v, %v", got, err)
				}
			} else {
				if !errors.Is(err, context.Canceled) || f.child.starts != 0 {
					t.Fatalf("cancelled Start = %+v, %v; spawns %d", got, err, f.child.starts)
				}
				if _, err := f.journal.Store.Get(t.Context(), "runtime-01", "op"); !errors.Is(err, journal.ErrOperationNotFound) {
					t.Fatalf("cancelled request left intent: %v", err)
				}
			}
		})
	}
}

func TestConcurrentStartSubmissionsSpawnOnce(t *testing.T) {
	for _, sameID := range []bool{false, true} {
		t.Run(fmt.Sprintf("sameID=%t", sameID), func(t *testing.T) {
			f := newStartFixture(t)
			gate := make(chan struct{})
			results := make(chan error, 32)
			var callers sync.WaitGroup
			for i := range 32 {
				callers.Add(1)
				go func() {
					defer callers.Done()
					<-gate
					id := "same-id"
					if !sameID {
						id = fmt.Sprintf("op-%d", i)
					}
					_, err := f.starter.Start(t.Context(), startRequest(id))
					results <- err
				}()
			}
			close(gate)
			callers.Wait()
			close(results)
			var successes int
			for err := range results {
				if err == nil {
					successes++
				} else if sameID || !errors.Is(err, journal.ErrOperationStateConflict) {
					t.Fatalf("concurrent submission: %v", err)
				}
			}
			want := 1
			if sameID {
				want = 32
			}
			if successes != want || f.child.starts != 1 {
				t.Fatalf("successes %d, spawns %d", successes, f.child.starts)
			}
			var intents int
			for _, event := range f.events {
				if event == "begin" {
					intents++
				}
			}
			if intents != 1 {
				t.Fatalf("concurrent submissions created %d intents", intents)
			}
		})
	}
}

func TestCloseDrainsAcceptedExecutionBeforeClosingJournal(t *testing.T) {
	f := newStartFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	f.child.onStart = func(context.Context, cpaprocess.StartSpec) error {
		close(entered)
		<-release
		return nil
	}
	started, closed := make(chan error, 1), make(chan error, 1)
	go func() { _, err := f.starter.Start(t.Context(), startRequest("op")); started <- err }()
	<-entered
	go func() { closed <- f.starter.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("journal closed during accepted execution: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if err := f.starter.Close(); err != nil || f.journal.closeCalls != 1 {
		t.Fatalf("Close = %v, calls %d", err, f.journal.closeCalls)
	}
	if _, err := f.starter.Start(t.Context(), startRequest("later")); !errors.Is(err, ErrPersistenceUnavailable) {
		t.Fatalf("Start after Close = %v", err)
	}
	if _, err := f.journal.Store.Get(t.Context(), "runtime-01", "op"); err == nil {
		t.Fatal("Close left journal open")
	}
	if f.child.observation.State != cpaprocess.StateRunning ||
		!reflect.DeepEqual(f.events[len(f.events)-2:], []string{"complete:succeeded", "close"}) {
		t.Fatalf("shutdown did not drain terminal evidence without stopping child: %v", f.events)
	}
}

func TestRealSpawnFailureKeepsProcessUnownedAndPersistsFailure(t *testing.T) {
	f := newStartFixture(t)
	child := &cpaprocess.Manager{}
	f.starter.process = child
	f.starter.executable = filepath.Join(t.TempDir(), "missing-cpa")
	_, err := f.starter.Start(t.Context(), startRequest("op"))
	if !errors.Is(err, cpaprocess.ErrSpawnFailed) || child.Observe().State != cpaprocess.StateNotStarted {
		t.Fatalf("failed spawn = %v, observation %+v", err, child.Observe())
	}
	got, err := f.starter.Start(t.Context(), startRequest("op"))
	if err != nil || got.State != journal.StateFailed || got.FailureCode != "process_start_failed" {
		t.Fatalf("failed replay = %+v, %v", got, err)
	}
	if _, err := os.Stat(f.starter.executable); !os.IsNotExist(err) {
		t.Fatalf("unexpected test executable: %v", err)
	}
}

type startFixture struct {
	events  []string
	path    string
	journal *recordingJournal
	child   *fakeProcess
	starter *Executor
}

func newStartFixture(t *testing.T) *startFixture {
	t.Helper()
	f := &startFixture{path: filepath.Join(t.TempDir(), "operations.sqlite")}
	store, err := journal.Open(t.Context(), f.path, journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f.journal = &recordingJournal{Store: store, events: &f.events}
	f.child = &fakeProcess{events: &f.events, observation: cpaprocess.Observation{State: cpaprocess.StateNotStarted}}
	f.starter, err = NewExecutor(journal.Authority{RuntimeIdentity: "runtime-01", RuntimeGeneration: 41}, f.journal, f.child, "supervisor-local-cpa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.starter.Close() })
	return f
}

func startRequest(id string) StartRequest {
	return StartRequest{OperationID: id, ExpectedRuntimeIdentity: "runtime-01", ExpectedRuntimeGeneration: 41}
}

func startIntent(id string) journal.Intent {
	return journal.Intent{OperationID: id, OperationType: "start", ExpectedRuntimeIdentity: "runtime-01",
		ExpectedRuntimeGeneration: 41, RequestFingerprint: sha256.Sum256([]byte("runtime.start/v1:{}"))}
}

type recordingJournal struct {
	*journal.Store
	events      *[]string
	failAt      string
	beforeBegin func()
	afterBegin  func()
	closeCalls  int
}

func (j *recordingJournal) Resolve(ctx context.Context, a journal.Authority, i journal.Intent) (journal.Operation, bool, error) {
	*j.events = append(*j.events, "resolve")
	if j.failAt == "resolve" {
		return journal.Operation{}, false, injectedFailure
	}
	return j.Store.Resolve(ctx, a, i)
}

func (j *recordingJournal) Begin(ctx context.Context, a journal.Authority, i journal.Intent) (journal.Operation, bool, error) {
	*j.events = append(*j.events, "begin")
	if j.failAt == "begin" {
		return journal.Operation{}, false, injectedFailure
	}
	if j.beforeBegin != nil {
		j.beforeBegin()
	}
	op, created, err := j.Store.Begin(ctx, a, i)
	if err == nil && created && j.afterBegin != nil {
		j.afterBegin()
	}
	return op, created, err
}

func (j *recordingJournal) MarkRunning(ctx context.Context, identity, id string) (journal.Operation, error) {
	*j.events = append(*j.events, "running")
	if j.failAt == "running" {
		return journal.Operation{}, injectedFailure
	}
	return j.Store.MarkRunning(ctx, identity, id)
}

func (j *recordingJournal) Complete(ctx context.Context, identity, id string, state journal.State, code string) (journal.Operation, error) {
	*j.events = append(*j.events, "complete:"+string(state))
	if j.failAt == "complete" {
		return journal.Operation{}, injectedFailure
	}
	return j.Store.Complete(ctx, identity, id, state, code)
}

func (j *recordingJournal) Close() error {
	*j.events = append(*j.events, "close")
	j.closeCalls++
	return j.Store.Close()
}

type fakeProcess struct {
	events      *[]string
	observation cpaprocess.Observation
	starts      int
	stops       int
	onStart     func(context.Context, cpaprocess.StartSpec) error
	onPrepare   func()
	onStop      func(context.Context) error
}

func (p *fakeProcess) Observe() cpaprocess.Observation {
	*p.events = append(*p.events, "observe")
	return p.observation
}

func (p *fakeProcess) Start(ctx context.Context, spec cpaprocess.StartSpec) (cpaprocess.Observation, error) {
	*p.events = append(*p.events, "spawn")
	p.starts++
	if p.onStart != nil {
		if err := p.onStart(ctx, spec); err != nil {
			return p.observation, err
		}
	}
	p.observation = cpaprocess.Observation{State: cpaprocess.StateRunning, PID: 123}
	return p.observation, nil
}

func (p *fakeProcess) PrepareStop() (cpaprocess.StopTarget, error) {
	*p.events = append(*p.events, "prepare-stop")
	if p.onPrepare != nil {
		p.onPrepare()
	}
	if p.observation.State != cpaprocess.StateRunning {
		return nil, cpaprocess.ErrStateConflict
	}
	return &fakeStopTarget{process: p}, nil
}

func (p *fakeProcess) ExitEvents() <-chan cpaprocess.ExitEvent { return nil }

type fakeStopTarget struct {
	process  *fakeProcess
	released bool
}

func (target *fakeStopTarget) Terminate(ctx context.Context) (cpaprocess.Observation, error) {
	*target.process.events = append(*target.process.events, "terminate")
	if target.process.observation.State == cpaprocess.StateExited {
		return target.process.observation, nil
	}
	target.process.stops++
	if target.process.onStop != nil {
		if err := target.process.onStop(ctx); err != nil {
			return target.process.observation, err
		}
	}
	target.process.observation = cpaprocess.Observation{State: cpaprocess.StateExited}
	return target.process.observation, nil
}

func (target *fakeStopTarget) Release() {
	if target.released {
		return
	}
	target.released = true
}
