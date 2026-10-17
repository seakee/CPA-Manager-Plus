package lifecycle

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
)

const (
	maxAutomaticRecoveryAttempts = 3
	automaticRecoveryDelay       = time.Second
)

// RecoveryState is orthogonal to Runtime availability. It reports only the
// current Supervisor incarnation's bounded automatic recovery authority.
type RecoveryState string

const (
	RecoveryStateInactive           RecoveryState = "inactive"
	RecoveryStateArmed              RecoveryState = "armed"
	RecoveryStateRecovering         RecoveryState = "recovering"
	RecoveryStateManualIntervention RecoveryState = "manual_intervention"
)

type RecoveryStatus struct {
	State             RecoveryState
	AttemptsRemaining int
}

type recoveryDelayWaiter func(context.Context) bool
type recoveryOperationIDSource func() (string, error)

func waitAutomaticRecoveryDelay(ctx context.Context) bool {
	timer := time.NewTimer(automaticRecoveryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func randomRecoveryOperationID() (string, error) {
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return "auto-recovery-" + base64.RawURLEncoding.EncodeToString(entropy[:]), nil
}

// RecoveryStatus returns policy observation only. It never observes the
// process, probes readiness, writes the journal, or schedules recovery.
func (e *Executor) RecoveryStatus() RecoveryStatus {
	e.recoveryMu.Lock()
	defer e.recoveryMu.Unlock()
	state := e.recoveryState
	if state == "" {
		state = RecoveryStateInactive
	}
	return RecoveryStatus{State: state, AttemptsRemaining: e.recoveryAttempts}
}

func (e *Executor) observeConfirmedExits(events <-chan cpaprocess.ExitEvent) {
	defer e.recoveryWorkers.Done()
	for {
		select {
		case <-e.recoveryContext.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			e.reconcileConfirmedExit(event.InstanceID)
		}
	}
}

// reconcileConfirmedExit deliberately uses only the exact Wait/reap instance.
// Readiness observations, listener failures, and StateUnknown never enter here.
func (e *Executor) reconcileConfirmedExit(instanceID uint64) {
	if instanceID == 0 {
		return
	}
	observation := e.process.Observe()
	if observation.State != cpaprocess.StateExited || observation.InstanceID != instanceID {
		return
	}
	e.scheduleAutomaticRecovery(instanceID)
}

func (e *Executor) scheduleAutomaticRecovery(instanceID uint64) {
	e.recoveryMu.Lock()
	if e.closed.Load() || e.recoveryState != RecoveryStateArmed ||
		e.recoveryInstanceID != instanceID {
		e.recoveryMu.Unlock()
		return
	}
	if e.recoveryAttempts <= 0 {
		e.disableRecoveryLocked(RecoveryStateManualIntervention)
		e.recoveryMu.Unlock()
		return
	}
	epoch := e.recoveryEpoch
	e.recoveryState = RecoveryStateRecovering
	timerContext, cancel := context.WithCancel(e.recoveryContext)
	e.cancelRecoveryTimer = cancel
	e.recoveryWorkers.Add(1)
	e.recoveryMu.Unlock()

	go func() {
		defer e.recoveryWorkers.Done()
		defer cancel()
		if !e.waitForRecoveryDelay(timerContext) {
			return
		}
		e.executeAutomaticRecovery(epoch, instanceID)
	}()
}

// executeAutomaticRecovery enters the same gate as Start/Stop/Restart. It
// rechecks admission, epoch, exact crashed child and budget before creating
// durable private evidence. Holding recoveryMu through Begin/MarkRunning gives
// CloseAdmission a linearization boundary: either the attempt is durably
// running first, or shutdown disables it before any intent is written.
func (e *Executor) executeAutomaticRecovery(epoch uint64, crashedInstanceID uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed.Load() {
		return
	}
	if e.activeSelectionAmbiguous {
		e.disableRecovery(RecoveryStateManualIntervention)
		return
	}

	e.recoveryMu.Lock()
	if e.closed.Load() || e.recoveryEpoch != epoch ||
		e.recoveryState != RecoveryStateRecovering ||
		e.recoveryInstanceID != crashedInstanceID || e.recoveryAttempts <= 0 {
		e.recoveryMu.Unlock()
		return
	}
	observation := e.process.Observe()
	if observation.State != cpaprocess.StateExited || observation.InstanceID != crashedInstanceID {
		e.recoveryMu.Unlock()
		return
	}

	operationID, err := e.newRecoveryOperationID()
	if err != nil {
		e.disableRecoveryLocked(RecoveryStateManualIntervention)
		e.recoveryMu.Unlock()
		return
	}
	intent := journal.Intent{
		OperationID:               operationID,
		OperationType:             "auto_recovery",
		ExpectedRuntimeIdentity:   e.authority.RuntimeIdentity,
		ExpectedRuntimeGeneration: e.authority.RuntimeGeneration,
		RequestFingerprint:        sha256.Sum256([]byte("runtime.auto-recovery/v1:{}")),
	}
	executionContext := context.Background()
	_, created, err := e.journal.Begin(executionContext, e.authority, intent)
	if err != nil || !created {
		e.disableRecoveryLocked(RecoveryStateManualIntervention)
		e.recoveryMu.Unlock()
		return
	}
	if _, err := e.journal.MarkRunning(executionContext, e.authority.RuntimeIdentity, operationID); err != nil {
		e.disableRecoveryLocked(RecoveryStateManualIntervention)
		e.recoveryMu.Unlock()
		return
	}

	// The bounded privilege is consumed only after running evidence commits and
	// immediately before the automatic spawn side effect.
	e.recoveryAttempts--
	e.cancelRecoveryTimer = nil
	e.recoveryMu.Unlock()

	started, spawnErr := e.startProcess(executionContext, e.currentExecutableLocked())
	if spawnErr != nil {
		if _, err := e.journal.Complete(
			executionContext,
			e.authority.RuntimeIdentity,
			operationID,
			journal.StateFailed,
			"process_recovery_start_failed",
		); err != nil {
			e.failRecoveryEpoch(epoch)
			return
		}
		e.retryRecoveryEpoch(epoch, crashedInstanceID)
		return
	}

	if _, err := e.journal.Complete(
		executionContext,
		e.authority.RuntimeIdentity,
		operationID,
		journal.StateSucceeded,
		"",
	); err != nil {
		// The replacement may be running. Never kill it or retry this attempt
		// when terminal durable evidence is ambiguous.
		e.failRecoveryEpoch(epoch)
		return
	}
	e.bindRecoveredChild(epoch, started)
}

func (e *Executor) bindRecoveredChild(epoch uint64, started cpaprocess.Observation) {
	e.recoveryMu.Lock()
	if e.closed.Load() || e.recoveryEpoch != epoch || e.recoveryState != RecoveryStateRecovering {
		e.recoveryMu.Unlock()
		return
	}
	if started.State != cpaprocess.StateRunning || started.InstanceID == 0 {
		e.disableRecoveryLocked(RecoveryStateManualIntervention)
		e.recoveryMu.Unlock()
		return
	}
	e.recoveryInstanceID = started.InstanceID
	if e.recoveryAttempts == 0 {
		e.disableRecoveryLocked(RecoveryStateManualIntervention)
		e.recoveryMu.Unlock()
		return
	}
	e.recoveryState = RecoveryStateArmed
	e.recoveryMu.Unlock()

	// The replacement may have exited and been reaped before terminal journal
	// success. Re-observe exact identity so a fast exit is neither lost nor
	// double-scheduled with its queued ExitEvent.
	e.reconcileConfirmedExit(started.InstanceID)
}

func (e *Executor) retryRecoveryEpoch(epoch uint64, crashedInstanceID uint64) {
	e.recoveryMu.Lock()
	if e.closed.Load() || e.recoveryEpoch != epoch || e.recoveryState != RecoveryStateRecovering {
		e.recoveryMu.Unlock()
		return
	}
	if e.recoveryAttempts == 0 {
		e.disableRecoveryLocked(RecoveryStateManualIntervention)
		e.recoveryMu.Unlock()
		return
	}
	e.recoveryState = RecoveryStateArmed
	e.recoveryMu.Unlock()
	e.scheduleAutomaticRecovery(crashedInstanceID)
}

func (e *Executor) failRecoveryEpoch(epoch uint64) {
	e.recoveryMu.Lock()
	defer e.recoveryMu.Unlock()
	if e.recoveryEpoch == epoch {
		e.disableRecoveryLocked(RecoveryStateManualIntervention)
	}
}

// armFreshRecovery is called only after a new explicit Start/Restart terminal
// succeeded result is durable. Automatic success uses bindRecoveredChild and
// therefore never resets the attempt budget.
func (e *Executor) armFreshRecovery(started cpaprocess.Observation) {
	e.recoveryMu.Lock()
	if e.closed.Load() || started.State != cpaprocess.StateRunning || started.InstanceID == 0 {
		e.disableRecoveryLocked(RecoveryStateManualIntervention)
		e.recoveryMu.Unlock()
		return
	}
	if !e.advanceRecoveryEpochLocked() {
		e.disableRecoveryLocked(RecoveryStateManualIntervention)
		e.recoveryMu.Unlock()
		return
	}
	e.recoveryState = RecoveryStateArmed
	e.recoveryAttempts = maxAutomaticRecoveryAttempts
	e.recoveryInstanceID = started.InstanceID
	e.recoveryMu.Unlock()
	e.reconcileConfirmedExit(started.InstanceID)
}

// failRecoveryForAmbiguousBegin preserves the current lease when Begin proves
// that no intent could have committed. Other persistence errors remain
// ambiguous and fail closed before any new lifecycle side effect.
func (e *Executor) failRecoveryForAmbiguousBegin(err error) {
	switch {
	case errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, journal.ErrRuntimeIdentityMismatch),
		errors.Is(err, journal.ErrStaleRuntimeGeneration),
		errors.Is(err, journal.ErrOperationIDConflict),
		errors.Is(err, journal.ErrOperationStateConflict):
		return
	default:
		e.disableRecovery(RecoveryStateManualIntervention)
	}
}

func (e *Executor) disableRecovery(state RecoveryState) {
	e.recoveryMu.Lock()
	defer e.recoveryMu.Unlock()
	e.disableRecoveryLocked(state)
}

func (e *Executor) disableRecoveryLocked(state RecoveryState) {
	if e.cancelRecoveryTimer != nil {
		e.cancelRecoveryTimer()
		e.cancelRecoveryTimer = nil
	}
	if e.recoveryEpoch != ^uint64(0) {
		e.recoveryEpoch++
	}
	e.recoveryState = state
	e.recoveryAttempts = 0
	e.recoveryInstanceID = 0
}

func (e *Executor) advanceRecoveryEpochLocked() bool {
	if e.cancelRecoveryTimer != nil {
		e.cancelRecoveryTimer()
		e.cancelRecoveryTimer = nil
	}
	if e.recoveryEpoch == ^uint64(0) {
		return false
	}
	e.recoveryEpoch++
	return true
}
