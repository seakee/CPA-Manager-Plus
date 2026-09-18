package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
)

func TestObserveUpdateOperationReadsEveryRetainedStateAcrossGenerations(t *testing.T) {
	for _, operationType := range []string{UpdateOperationPrepare, UpdateOperationActivate} {
		for _, state := range []journal.State{
			journal.StateAccepted,
			journal.StateRunning,
			journal.StateSucceeded,
			journal.StateFailed,
		} {
			t.Run(operationType+"/"+string(state), func(t *testing.T) {
				fixture := newStartFixture(t)
				intent := updateIntent("op", operationType, "7.3.4", 41)
				operation, _, err := fixture.journal.Store.Begin(t.Context(), fixture.starter.authority, intent)
				if err != nil {
					t.Fatal(err)
				}
				switch state {
				case journal.StateRunning:
					operation, err = fixture.journal.Store.MarkRunning(t.Context(), "runtime-01", "op")
				case journal.StateSucceeded:
					operation, err = fixture.journal.Store.Complete(t.Context(), "runtime-01", "op", state, "")
				case journal.StateFailed:
					operation, err = fixture.journal.Store.Complete(t.Context(), "runtime-01", "op", state, "staging_failed")
				}
				if err != nil {
					t.Fatal(err)
				}

				fixture.events = nil
				beforeRows := observationJournalRowCount(t, fixture.path)
				for _, generation := range []uint64{42, 7} {
					fixture.starter.authority.RuntimeGeneration = generation
					request := observeUpdateRequest("op", operationType, "7.3.4", generation)
					got, err := fixture.starter.ObserveUpdateOperation(t.Context(), request)
					if err != nil || !reflect.DeepEqual(got, operation) || got.RuntimeGeneration != 41 {
						t.Fatalf("generation %d observation = %+v, %v; want unchanged %+v", generation, got, err, operation)
					}
				}
				if !reflect.DeepEqual(fixture.events, []string{"resolve", "resolve"}) {
					t.Fatalf("observation events = %v", fixture.events)
				}
				stored, err := fixture.journal.Store.Get(t.Context(), "runtime-01", "op")
				if err != nil || !reflect.DeepEqual(stored, operation) || fixture.child.starts != 0 || fixture.child.stops != 0 ||
					observationJournalRowCount(t, fixture.path) != beforeRows {
					t.Fatalf("stored operation changed: %+v, %v; starts=%d stops=%d", stored, err, fixture.child.starts, fixture.child.stops)
				}
			})
		}
	}
}

func TestObserveUpdateOperationFencesQueryAndFingerprintWithoutWriting(t *testing.T) {
	fixture := newStartFixture(t)
	request := observeUpdateRequest("op", UpdateOperationPrepare, "7.3.4", 41)

	if _, err := fixture.starter.ObserveUpdateOperation(t.Context(), request); !errors.Is(err, journal.ErrOperationNotFound) {
		t.Fatalf("before durable Begin error = %v", err)
	}
	if _, err := fixture.journal.Store.Get(t.Context(), "runtime-01", "op"); !errors.Is(err, journal.ErrOperationNotFound) {
		t.Fatalf("observation created operation: %v", err)
	}
	if observationJournalRowCount(t, fixture.path) != 0 {
		t.Fatal("missing-operation observation created journal rows")
	}

	intent := updateIntent("op", UpdateOperationPrepare, "7.3.4", 41)
	accepted, _, err := fixture.journal.Store.Begin(t.Context(), fixture.starter.authority, intent)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fixture.starter.ObserveUpdateOperation(t.Context(), request)
	if err != nil || !reflect.DeepEqual(got, accepted) {
		t.Fatalf("after durable Begin = %+v, %v", got, err)
	}

	conflicts := []ObserveUpdateOperationRequest{
		observeUpdateRequest("op", UpdateOperationPrepare, "7.3.5", 41),
		observeUpdateRequest("op", UpdateOperationActivate, "7.3.4", 41),
	}
	for _, conflict := range conflicts {
		if _, err := fixture.starter.ObserveUpdateOperation(t.Context(), conflict); !errors.Is(err, journal.ErrOperationIDConflict) {
			t.Fatalf("conflicting observation error = %v", err)
		}
	}

	identityMismatch := request
	identityMismatch.ExpectedRuntimeIdentity = "other-runtime"
	if _, err := fixture.starter.ObserveUpdateOperation(t.Context(), identityMismatch); !errors.Is(err, journal.ErrRuntimeIdentityMismatch) {
		t.Fatalf("identity mismatch error = %v", err)
	}
	stale := request
	stale.ExpectedRuntimeGeneration = 40
	if _, err := fixture.starter.ObserveUpdateOperation(t.Context(), stale); !errors.Is(err, journal.ErrStaleRuntimeGeneration) {
		t.Fatalf("stale generation error = %v", err)
	}
	stored, err := fixture.journal.Store.Get(t.Context(), "runtime-01", "op")
	if err != nil || !reflect.DeepEqual(stored, accepted) || observationJournalRowCount(t, fixture.path) != 1 {
		t.Fatalf("conflicting queries changed durable evidence: %+v, %v", stored, err)
	}
	for _, event := range fixture.events {
		if event != "resolve" {
			t.Fatalf("observation performed side effects: %v", fixture.events)
		}
	}
}

func TestObserveUpdateOperationAfterDisconnectBeforeDurableBegin(t *testing.T) {
	for _, operationType := range []string{UpdateOperationPrepare, UpdateOperationActivate} {
		t.Run(operationType, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			var fixture *startFixture
			var err error
			if operationType == UpdateOperationPrepare {
				f := newPrepareFixture(t, activeArtifactA)
				fixture = f.startFixture
				_, err = f.starter.PrepareUpdate(ctx, prepareUpdateRequest("disconnected", "7.3.4", activeArtifactA))
				if f.preparer.resolveCalls != 0 || f.preparer.stageCalls != 0 {
					t.Fatal("cancelled prepare reached release or staging")
				}
			} else {
				f := newActivationFixture(t)
				fixture = f.startFixture
				_, err = f.starter.ActivateUpdate(ctx, activateRequest("disconnected", "7.3.4"))
				if f.child.starts != 0 || f.child.stops != 0 {
					t.Fatal("cancelled activation changed the child")
				}
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("disconnected mutation error = %v", err)
			}
			if _, err := fixture.starter.ObserveUpdateOperation(t.Context(),
				observeUpdateRequest("disconnected", operationType, "7.3.4", 41)); !errors.Is(err, journal.ErrOperationNotFound) {
				t.Fatalf("before-Begin observation = %v", err)
			}
			if !reflect.DeepEqual(fixture.events, []string{"resolve"}) || observationJournalRowCount(t, fixture.path) != 0 {
				t.Fatalf("observation started an operation: %v", fixture.events)
			}
		})
	}
}

func TestObserveUpdateOperationAfterDisconnectReadsAcceptedAndRunningWithoutExecutionGate(t *testing.T) {
	for _, operationType := range []string{UpdateOperationPrepare, UpdateOperationActivate} {
		t.Run(operationType, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var fixture *startFixture
			var execute func() (journal.Operation, error)
			var onRunning func(func())
			if operationType == UpdateOperationPrepare {
				f := newPrepareFixture(t, activeArtifactA)
				fixture = f.startFixture
				execute = func() (journal.Operation, error) {
					return f.starter.PrepareUpdate(ctx, prepareUpdateRequest("disconnected", "7.3.4", activeArtifactA))
				}
				onRunning = func(hook func()) { f.preparer.stageHook = hook }
			} else {
				f := newActivationFixture(t)
				fixture = f.startFixture
				execute = func() (journal.Operation, error) {
					return f.starter.ActivateUpdate(ctx, activateRequest("disconnected", "7.3.4"))
				}
				onRunning = func(hook func()) { f.ready.hook = func(string) { hook() } }
			}
			request := observeUpdateRequest("disconnected", operationType, "7.3.4", 41)
			states := []journal.State{}
			observe := func(want journal.State) {
				before, err := fixture.journal.Store.Get(t.Context(), "runtime-01", "disconnected")
				if err != nil {
					t.Fatal(err)
				}
				beforeEvents := len(fixture.events)
				got := observeUpdateWithinDeadline(t, fixture.starter, request)
				if got.State != want || !reflect.DeepEqual(got, before) ||
					!reflect.DeepEqual(fixture.events[beforeEvents:], []string{"resolve"}) ||
					observationJournalRowCount(t, fixture.path) != 1 {
					t.Fatalf("in-flight observation = %+v, events=%v", got, fixture.events[beforeEvents:])
				}
				states = append(states, got.State)
			}
			fixture.journal.afterBegin = func() {
				cancel()
				observe(journal.StateAccepted)
			}
			onRunning(func() { observe(journal.StateRunning) })
			terminal, err := execute()
			if err != nil || terminal.State != journal.StateSucceeded ||
				!reflect.DeepEqual(states, []journal.State{journal.StateAccepted, journal.StateRunning}) {
				t.Fatalf("detached execution = %+v, %v; observed=%v", terminal, err, states)
			}
			observe(journal.StateSucceeded)
		})
	}
}

func TestObserveUpdateOperationReportsJournalUnavailableWithoutMutation(t *testing.T) {
	fixture := newStartFixture(t)
	fixture.journal.failAt = "resolve"
	if _, err := fixture.starter.ObserveUpdateOperation(t.Context(),
		observeUpdateRequest("op", UpdateOperationPrepare, "7.3.4", 41)); !errors.Is(err, ErrPersistenceUnavailable) {
		t.Fatalf("unavailable journal = %v", err)
	}
	if !reflect.DeepEqual(fixture.events, []string{"resolve"}) || observationJournalRowCount(t, fixture.path) != 0 {
		t.Fatalf("unavailable observation wrote evidence: %v", fixture.events)
	}
}

func observeUpdateWithinDeadline(t *testing.T, executor *Executor, request ObserveUpdateOperationRequest) journal.Operation {
	t.Helper()
	type result struct {
		operation journal.Operation
		err       error
	}
	done := make(chan result, 1)
	go func() {
		operation, err := executor.ObserveUpdateOperation(t.Context(), request)
		done <- result{operation, err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		return got.operation
	case <-time.After(time.Second):
		t.Fatal("observation blocked on an execution gate")
		return journal.Operation{}
	}
}

func observationJournalRowCount(t *testing.T, path string) int {
	t.Helper()
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	reader, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var count int
	if err := reader.QueryRowContext(t.Context(), "select count(*) from operations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func observeUpdateRequest(id, operationType, target string, generation uint64) ObserveUpdateOperationRequest {
	return ObserveUpdateOperationRequest{
		OperationID:               id,
		ExpectedRuntimeIdentity:   "runtime-01",
		ExpectedRuntimeGeneration: generation,
		OperationType:             operationType,
		TargetVersion:             target,
	}
}

func updateIntent(id, operationType, target string, generation uint64) journal.Intent {
	return journal.Intent{
		OperationID:               id,
		OperationType:             operationType,
		ExpectedRuntimeIdentity:   "runtime-01",
		ExpectedRuntimeGeneration: generation,
		RequestFingerprint:        updateOperationFingerprint(operationType, target),
	}
}
