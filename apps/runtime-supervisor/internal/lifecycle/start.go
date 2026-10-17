// Package lifecycle orchestrates Supervisor-private, durable CPA mutations.
package lifecycle

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/selection"
)

var (
	ErrInvalidRequest         = errors.New("invalid lifecycle request")
	ErrPersistenceUnavailable = errors.New("operation persistence unavailable")
	ErrExecutionFailed        = errors.New("lifecycle execution failed")
)

// StartRequest optionally fences the selected executable identity. Execution
// paths and arguments still belong exclusively to Supervisor-local configuration.
type StartRequest struct {
	OperationID               string      `json:"operationId"`
	ExpectedRuntimeIdentity   string      `json:"expectedRuntimeIdentity"`
	ExpectedRuntimeGeneration uint64      `json:"expectedRuntimeGeneration"`
	ExpectedActiveArtifactID  artifact.ID `json:"expectedActiveArtifactId,omitempty"`
}

func (r StartRequest) Validate() error {
	if r.ExpectedActiveArtifactID != "" && !r.ExpectedActiveArtifactID.IsValid() {
		return ErrInvalidRequest
	}
	return validateRequest(r.OperationID, r.ExpectedRuntimeIdentity, r.ExpectedRuntimeGeneration)
}

// StopRequest has an empty typed payload. The owned child target and hard
// termination policy belong exclusively to the Supervisor.
type StopRequest struct {
	OperationID               string `json:"operationId"`
	ExpectedRuntimeIdentity   string `json:"expectedRuntimeIdentity"`
	ExpectedRuntimeGeneration uint64 `json:"expectedRuntimeGeneration"`
}

func (r StopRequest) Validate() error {
	return validateRequest(r.OperationID, r.ExpectedRuntimeIdentity, r.ExpectedRuntimeGeneration)
}

func validateRequest(operationID, expectedIdentity string, expectedGeneration uint64) error {
	if operationID == "" || !utf8.ValidString(operationID) || len(operationID) > 128 ||
		strings.TrimSpace(expectedIdentity) == "" || !utf8.ValidString(expectedIdentity) || expectedGeneration == 0 {
		return ErrInvalidRequest
	}
	return nil
}

type operationJournal interface {
	Resolve(context.Context, journal.Authority, journal.Intent) (journal.Operation, bool, error)
	Begin(context.Context, journal.Authority, journal.Intent) (journal.Operation, bool, error)
	MarkRunning(context.Context, string, string) (journal.Operation, error)
	Complete(context.Context, string, string, journal.State, string) (journal.Operation, error)
	Close() error
}

type process interface {
	Observe() cpaprocess.Observation
	Start(context.Context, cpaprocess.StartSpec) (cpaprocess.Observation, error)
	PrepareStop() (cpaprocess.StopTarget, error)
	ExitEvents() <-chan cpaprocess.ExitEvent
}

// Executor owns the shared Start/Stop/Restart serialization and resources for
// one Supervisor incarnation. The shared lock covers lifecycle execution
// evidence, while updater staging has its own narrow gate and cpaprocess
// remains the final ownership fence.
type Executor struct {
	mu                       sync.Mutex
	authority                journal.Authority
	journal                  operationJournal
	process                  process
	executable               string
	artifact                 activeArtifactRefresher
	updates                  updatePreparer
	active                   selection.Descriptor
	selections               activeSelectionStore
	readiness                activationReadiness
	activeSelectionAmbiguous bool
	closed                   atomic.Bool
	closeOnce                sync.Once
	closeErr                 error
	destination              *destination

	// Prepare staging is serialized independently from lifecycle/recovery
	// execution. The admission mutex makes worker registration and shutdown a
	// single boundary: Close cannot close the journal while an accepted prepare
	// is still able to reach terminal persistence.
	prepareMu          sync.Mutex
	prepareAdmissionMu sync.Mutex
	prepareWorkers     sync.WaitGroup

	recoveryMu             sync.Mutex
	recoveryContext        context.Context
	cancelRecovery         context.CancelFunc
	recoveryWorkers        sync.WaitGroup
	recoveryEpoch          uint64
	recoveryState          RecoveryState
	recoveryAttempts       int
	recoveryInstanceID     uint64
	cancelRecoveryTimer    context.CancelFunc
	waitForRecoveryDelay   recoveryDelayWaiter
	newRecoveryOperationID recoveryOperationIDSource
}

func NewExecutor(authority journal.Authority, store operationJournal, child process, executable string) (*Executor, error) {
	if strings.TrimSpace(authority.RuntimeIdentity) == "" || authority.RuntimeGeneration == 0 || store == nil || child == nil {
		return nil, errors.New("lifecycle mutations require Supervisor authority, journal and child manager")
	}
	if strings.TrimSpace(executable) == "" || strings.ContainsRune(executable, '\x00') {
		return nil, errors.New("lifecycle mutations require a local CPA executable without NUL")
	}
	recoveryContext, cancelRecovery := context.WithCancel(context.Background())
	executor := &Executor{
		authority:              authority,
		journal:                store,
		process:                child,
		executable:             executable,
		recoveryContext:        recoveryContext,
		cancelRecovery:         cancelRecovery,
		recoveryState:          RecoveryStateInactive,
		waitForRecoveryDelay:   waitAutomaticRecoveryDelay,
		newRecoveryOperationID: randomRecoveryOperationID,
	}
	executor.recoveryWorkers.Add(1)
	go executor.observeConfirmedExits(child.ExitEvents())
	return executor, nil
}

func (e *Executor) Start(ctx context.Context, request StartRequest) (journal.Operation, error) {
	if err := request.Validate(); err != nil {
		return journal.Operation{}, err
	}
	if e.closed.Load() {
		return journal.Operation{}, ErrPersistenceUnavailable
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return journal.Operation{}, err
	}
	if e.closed.Load() {
		return journal.Operation{}, ErrPersistenceUnavailable
	}
	fingerprint := sha256.Sum256([]byte("runtime.start/v1:{}"))
	if request.ExpectedActiveArtifactID != "" {
		fingerprint = sha256.Sum256([]byte(`runtime.start/v1:{"expectedActiveArtifactId":"` + string(request.ExpectedActiveArtifactID) + `"}`))
	}
	intent := journal.Intent{
		OperationID:               request.OperationID,
		OperationType:             "start",
		ExpectedRuntimeIdentity:   request.ExpectedRuntimeIdentity,
		ExpectedRuntimeGeneration: request.ExpectedRuntimeGeneration,
		RequestFingerprint:        fingerprint,
	}
	operation, found, err := e.journal.Resolve(ctx, e.authority, intent)
	if err != nil {
		// Resolve is read-only and cannot supersede existing recovery authority.
		return journal.Operation{}, submissionError(err)
	}
	if found {
		// Retained accepted/running evidence is replayed, never resumed here.
		return operation, nil
	}
	if e.activeSelectionAmbiguous {
		return journal.Operation{}, journal.ErrOperationStateConflict
	}
	// This check shares the lifecycle lock with activation and the eventual
	// spawn. A same-generation A -> B selection change must not turn a Fresh
	// intent for A into permission to start B. Resolve remains first so retained
	// evidence replays without another spawn or a new artifact owner.
	if request.ExpectedActiveArtifactID != "" {
		if e.artifact == nil {
			return journal.Operation{}, ErrActiveArtifactUnavailable
		}
		if err := refreshExpectedActiveArtifact(e.artifact, e.currentDescriptorLocked(), request.ExpectedActiveArtifactID); err != nil {
			return journal.Operation{}, err
		}
	}
	switch e.process.Observe().State {
	case cpaprocess.StateNotStarted, cpaprocess.StateExited:
		// cpaprocess only publishes exited after a confirmed Wait/reap.
	default:
		return journal.Operation{}, journal.ErrOperationStateConflict
	}
	operation, created, err := e.journal.Begin(ctx, e.authority, intent)
	if err != nil {
		e.failRecoveryForAmbiguousBegin(err)
		return journal.Operation{}, submissionError(err)
	}
	if !created {
		return operation, nil
	}

	// Durable acceptance transfers execution to the Supervisor. Request
	// cancellation/deadlines/values no longer control the operation or child.
	// This remains synchronous; each journal write has its own bounded retry.
	executionCtx := context.Background()
	operation, err = e.journal.MarkRunning(executionCtx, e.authority.RuntimeIdentity, intent.OperationID)
	if err != nil {
		e.disableRecovery(RecoveryStateManualIntervention)
		return journal.Operation{}, fmt.Errorf("%w: %w", ErrPersistenceUnavailable, err)
	}
	// A newly accepted explicit run supersedes any prior crash timer. Only its
	// own durable terminal success may grant a fresh recovery epoch.
	e.disableRecovery(RecoveryStateInactive)
	started, spawnErr := e.startProcess(executionCtx, e.currentExecutableLocked())
	state, failureCode := journal.StateSucceeded, ""
	if spawnErr != nil {
		state, failureCode = journal.StateFailed, "process_start_failed"
	}
	result, err := e.journal.Complete(executionCtx, e.authority.RuntimeIdentity, intent.OperationID, state, failureCode)
	if err != nil {
		// Spawn may already have succeeded. Never retry it or kill the child
		// to compensate for missing terminal evidence.
		e.disableRecovery(RecoveryStateManualIntervention)
		return operation, fmt.Errorf("%w: record result: %w", ErrExecutionFailed, err)
	}
	if spawnErr != nil {
		e.disableRecovery(RecoveryStateInactive)
		return result, fmt.Errorf("%w: %w", ErrExecutionFailed, spawnErr)
	}
	e.armFreshRecovery(started)
	// Success means OS spawn and ownership publication, not Runtime readiness.
	return result, nil
}

func (e *Executor) currentExecutableLocked() string {
	return e.currentDescriptorLocked().ExecutablePath
}

func (e *Executor) currentDescriptorLocked() selection.Descriptor {
	if e.active.ExecutablePath != "" {
		return e.active
	}
	return selection.Bundled(e.executable, "")
}

func submissionError(err error) error {
	switch {
	case errors.Is(err, journal.ErrRuntimeIdentityMismatch),
		errors.Is(err, journal.ErrStaleRuntimeGeneration),
		errors.Is(err, journal.ErrOperationIDConflict):
		return err
	default:
		return fmt.Errorf("%w: %w", ErrPersistenceUnavailable, err)
	}
}

// CloseAdmission prevents new lifecycle mutations and prepare executions from
// entering their admission boundaries. Submissions already admitted recheck
// this state before recording durable intent; accepted work is drained by
// Close.
func (e *Executor) CloseAdmission() {
	e.prepareAdmissionMu.Lock()
	defer e.prepareAdmissionMu.Unlock()
	e.recoveryMu.Lock()
	defer e.recoveryMu.Unlock()
	if e.closed.Swap(true) {
		return
	}
	// Cancel exit observation and pending delays immediately. disableRecovery
	// shares this lock with automatic durable admission: an attempt already in
	// that critical section may finish, while every later attempt sees closed
	// before writing intent. Accepted work is drained by Close.
	e.cancelRecovery()
	e.disableRecoveryLocked(RecoveryStateInactive)
}

// Close drains any synchronous execution before closing the startup-owned
// journal, including after HTTP shutdown forcibly disconnects a caller. It
// rejects later submissions and does not stop the CPA child.
func (e *Executor) Close() error {
	e.CloseAdmission()
	e.recoveryWorkers.Wait()
	e.prepareWorkers.Wait()
	e.closeOnce.Do(func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.closeErr = e.journal.Close()
	})
	return e.closeErr
}
