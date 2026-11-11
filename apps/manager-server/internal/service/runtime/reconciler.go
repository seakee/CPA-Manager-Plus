package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

const (
	DefaultReconcileInterval = 5 * time.Second
	// DefaultReconcileStartAttempts bounds the automatic Start attempts issued
	// for one Supervisor generation. The Supervisor journal keeps a terminal
	// failure forever, so a retry has to use a new operation ID; the cap keeps a
	// permanently broken destination from spawning in a loop for the lifetime of
	// the generation.
	DefaultReconcileStartAttempts = 3
)

var (
	ErrReconcileObservationInvalid  = errors.New("Runtime reconcile observation is invalid")
	ErrReconcileOperationConflict   = errors.New("Runtime reconcile operation ID conflict")
	ErrReconcileReobserve           = errors.New("Runtime reconcile must re-observe")
	ErrReconcileOperationIncomplete = errors.New("Runtime reconcile operation is not durably succeeded")
	ErrReconcileStartExhausted      = errors.New("Runtime reconcile automatic Start attempts exhausted")
	ErrReconcileStartDeferred       = errors.New("Runtime reconcile automatic Start retry is deferred")
)

// reconcileStartRetryDelays is the wait before the second and third automatic
// Start attempt, indexed by the number of attempts that already failed. The
// first attempt is issued immediately: the Manager only reconciles after it
// observed the Runtime offline itself.
var reconcileStartRetryDelays = []time.Duration{15 * time.Second, time.Minute}

type ReconcileAction string

const (
	ReconcileActionNone  ReconcileAction = ""
	ReconcileActionStart ReconcileAction = "start"
	ReconcileActionStop  ReconcileAction = "stop"
)

type DesiredStateStore interface {
	LoadEmbeddedRuntimeDesiredState(context.Context) (model.EmbeddedRuntimeDesiredState, bool, error)
}

// CommittedArtifactStore is the optional durable authority of a Manager-owned
// installation: the engine artifact that installation committed to, independent
// of anything the Supervisor currently reports. A store that implements it makes
// the reconcile Start fence that artifact exactly as the Console Start fences the
// one it observed, so an automatic restart cannot launch an engine the
// installation never committed.
type CommittedArtifactStore interface {
	LoadEmbeddedRuntimeCommittedArtifact(context.Context) (model.RuntimeArtifactID, bool, error)
}

// LifecycleLocker is an optional mutual-exclusion boundary shared between
// explicit operator lifecycle mutations (Start/Stop/Restart) and automatic
// reconcile cycles. Holding the lock across the reconciler's authoritative
// desired-state read, Runtime observation, and Start/Stop submission as well
// as the operator's Supervisor submission and desired-state persistence makes
// both paths linearizable.
type LifecycleLocker interface {
	LockEmbeddedRuntimeLifecycle(ctx context.Context) (unlock func(), err error)
}

// reconcileAttemptKey scopes the bounded Start retries to one Supervisor
// generation and one desired-state revision.
type reconcileAttemptKey struct {
	identity   model.RuntimeIdentity
	generation model.RuntimeGeneration
	revision   uint64
}

// reconcileStartAttempts is Manager-process-local bookkeeping for the bounded
// automatic Start retries of one Supervisor generation. It never becomes the
// idempotency authority — that stays the Supervisor journal, keyed by the
// operation ID this type only decides when to rotate. failedAt being zero is
// what keeps an unresolved attempt (accepted, running, or unconfirmed) on its
// original operation ID forever.
type reconcileStartAttempts struct {
	key       reconcileAttemptKey
	issued    int
	failedAt  time.Time
	exhausted bool
}

type Reconciler struct {
	mu       sync.Mutex
	client   RuntimeClient
	desired  DesiredStateStore
	interval time.Duration
	logf     func(string, ...any)
	now      func() time.Time
	attempts reconcileStartAttempts
}

func NewReconciler(
	client RuntimeClient,
	desired DesiredStateStore,
	interval time.Duration,
	logf func(string, ...any),
) *Reconciler {
	if interval <= 0 {
		interval = DefaultReconcileInterval
	}
	return &Reconciler{client: client, desired: desired, interval: interval, logf: logf, now: time.Now}
}

func (r *Reconciler) lockLifecycle(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var unlockExternal func()
	if locker, ok := r.desired.(LifecycleLocker); ok {
		var err error
		unlockExternal, err = locker.LockEmbeddedRuntimeLifecycle(ctx)
		if err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Unlock()
			if unlockExternal != nil {
				unlockExternal()
			}
		})
	}, nil
}

var reconcileErrorRetryDelays = []time.Duration{
	200 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
}

func reconcileErrorRetryDelay(interval time.Duration, consecutiveErrors int) time.Duration {
	if consecutiveErrors <= 0 || consecutiveErrors > len(reconcileErrorRetryDelays) {
		return interval
	}
	delay := reconcileErrorRetryDelays[consecutiveErrors-1]
	if interval > 0 && delay > interval {
		return interval
	}
	return delay
}

// Run performs one initial reconcile and then retries at a bounded interval.
// Transient reconcile errors use a bounded short retry backoff before falling
// back to the regular poll interval so a transient startup store read error
// does not stall convergence for a full poll cycle. Cancellation only stops
// this worker; it never translates Manager shutdown into a Runtime Stop
// operation.
func (r *Reconciler) Run(ctx context.Context) {
	if r == nil || r.client == nil || r.desired == nil {
		return
	}
	lastOutcome := ""
	consecutiveErrors := 0
	for {
		if ctx.Err() != nil {
			return
		}
		err := r.ReconcileOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		outcome := ""
		wait := r.interval
		if err != nil {
			outcome = err.Error()
			if !errors.Is(err, ErrReconcileStartDeferred) && !errors.Is(err, ErrReconcileStartExhausted) {
				consecutiveErrors++
				wait = reconcileErrorRetryDelay(r.interval, consecutiveErrors)
			} else {
				consecutiveErrors = 0
			}
		} else {
			consecutiveErrors = 0
		}
		if outcome != lastOutcome && r.logf != nil {
			switch {
			case outcome != "":
				r.logf("embedded Runtime reconcile waiting: %v", err)
			case lastOutcome != "":
				r.logf("embedded Runtime reconcile recovered")
			}
		}
		lastOutcome = outcome

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func (r *Reconciler) ReconcileOnce(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	desired, found, err := r.desired.LoadEmbeddedRuntimeDesiredState(ctx)
	if err != nil {
		return fmt.Errorf("load embedded Runtime desired state: %w", err)
	}
	if !found {
		return nil
	}
	if err := desired.Validate(); err != nil {
		return fmt.Errorf("validate embedded Runtime desired state: %w", err)
	}
	unlock, err := r.lockLifecycle(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	desired, found, err = r.desired.LoadEmbeddedRuntimeDesiredState(ctx)
	if err != nil {
		return fmt.Errorf("load embedded Runtime desired state: %w", err)
	}
	if !found {
		return nil
	}
	if err := desired.Validate(); err != nil {
		return fmt.Errorf("validate embedded Runtime desired state: %w", err)
	}
	status, err := r.client.Status(ctx)
	if err != nil {
		return fmt.Errorf("observe embedded Runtime before reconcile: %w", err)
	}
	action, err := DecideReconcileAction(desired.DesiredLifecycle, status)
	if err != nil {
		return err
	}
	if action == ReconcileActionNone {
		// The observation satisfied the desired lifecycle for this generation, so
		// the retry budget of a previous failure is no longer owed to it.
		if status.State != model.RuntimeStateOffline {
			r.attempts = reconcileStartAttempts{}
		}
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	operationType := model.RuntimeOperationType(action)
	attempt := 1
	if operationType == model.RuntimeOperationStart {
		attempt, err = r.beginStartAttempt(desired.Revision, status)
		if err != nil {
			return err
		}
	}
	// The artifact is part of the intent: it changes the Supervisor's request
	// fingerprint, so it has to change the operation ID with it.
	expectedArtifact, err := r.startArtifact(ctx, operationType, status)
	if err != nil {
		return err
	}
	request := model.RuntimeMutationRequest{
		OperationID: StableReconcileOperationID(
			desired.Revision, status.Identity, status.Generation, operationType, expectedArtifact, attempt,
		),
		ExpectedRuntimeIdentity:   status.Identity,
		ExpectedRuntimeGeneration: status.Generation,
		ExpectedActiveArtifactID:  expectedArtifact,
	}
	var result model.RuntimeOperationResult
	switch action {
	case ReconcileActionStart:
		result, err = r.client.Start(ctx, request)
	case ReconcileActionStop:
		result, err = r.client.Stop(ctx, request)
	default:
		return fmt.Errorf("unsupported Runtime reconcile action %q", action)
	}
	if err != nil {
		return classifyMutationError(err)
	}
	if err := result.Validate(); err != nil {
		return fmt.Errorf("%w: invalid %s result: %v", ErrReconcileOperationIncomplete, action, err)
	}
	if result.OperationID != request.OperationID || result.OperationType != operationType ||
		result.RuntimeIdentity != request.ExpectedRuntimeIdentity {
		return fmt.Errorf("%w: %s result does not match the submitted reconcile operation",
			ErrReconcileOperationIncomplete, action)
	}
	switch result.State {
	case model.RuntimeOperationSucceeded:
		r.attempts = reconcileStartAttempts{}
		return nil
	case model.RuntimeOperationAccepted, model.RuntimeOperationRunning:
		return fmt.Errorf("%w: %s %s is retained in state %s",
			ErrReconcileOperationIncomplete, result.OperationType, result.OperationID, result.State)
	case model.RuntimeOperationFailed:
		failureCode := "unknown"
		if result.Failure != nil && strings.TrimSpace(result.Failure.Code) != "" {
			failureCode = result.Failure.Code
		}
		if operationType == model.RuntimeOperationStart {
			// Only this durable terminal failure releases the next bounded attempt:
			// the Supervisor keeps it forever, so replaying the same operation ID
			// could never execute Start again within this generation.
			r.recordStartFailure()
		}
		return fmt.Errorf("%w: %s %s failed with code %s",
			ErrReconcileOperationIncomplete, result.OperationType, result.OperationID, failureCode)
	default:
		return fmt.Errorf("%w: invalid returned operation state %q", ErrReconcileOperationIncomplete, result.State)
	}
}

// beginStartAttempt returns the attempt number whose operation ID this reconcile
// may submit. Rotating the operation ID is the only way to re-execute a Start the
// Supervisor already recorded as failed, so the rotation is fenced three ways:
// it happens only after a durable terminal failure of the previous attempt, only
// after that attempt's retry delay, and only up to DefaultReconcileStartAttempts.
// An unresolved attempt keeps its original ID, which preserves the idempotent
// replay the journal guarantees. Re-executing after a recorded failure is safe
// because Start re-checks the child ownership fence under the lifecycle lock: a
// child that outlived the reported spawn failure turns the new attempt into a
// state conflict instead of a second process.
func (r *Reconciler) beginStartAttempt(revision uint64, status model.RuntimeObservedStatus) (int, error) {
	key := reconcileAttemptKey{identity: status.Identity, generation: status.Generation, revision: revision}
	if r.attempts.key != key {
		r.attempts = reconcileStartAttempts{key: key}
	}
	state := &r.attempts
	if state.issued == 0 {
		state.issued = 1
		return state.issued, nil
	}
	if state.failedAt.IsZero() {
		return state.issued, nil
	}
	if state.exhausted {
		return 0, fmt.Errorf("%w: %d attempts failed for Runtime generation %d",
			ErrReconcileStartExhausted, state.issued, status.Generation)
	}
	if r.now().Sub(state.failedAt) < reconcileStartRetryDelay(state.issued) {
		return 0, fmt.Errorf("%w: attempt %d of %d follows a durable failure",
			ErrReconcileStartDeferred, state.issued+1, DefaultReconcileStartAttempts)
	}
	if state.issued >= DefaultReconcileStartAttempts {
		state.exhausted = true
		return 0, fmt.Errorf("%w: %d attempts failed for Runtime generation %d",
			ErrReconcileStartExhausted, state.issued, status.Generation)
	}
	state.issued++
	state.failedAt = time.Time{}
	return state.issued, nil
}

func (r *Reconciler) recordStartFailure() {
	if r.attempts.issued == 0 {
		return
	}
	r.attempts.failedAt = r.now()
	if r.attempts.issued >= DefaultReconcileStartAttempts {
		r.attempts.exhausted = true
	}
}

func reconcileStartRetryDelay(failedAttempts int) time.Duration {
	if failedAttempts <= 0 || len(reconcileStartRetryDelays) == 0 {
		return 0
	}
	index := failedAttempts - 1
	if index >= len(reconcileStartRetryDelays) {
		index = len(reconcileStartRetryDelays) - 1
	}
	return reconcileStartRetryDelays[index]
}

// startArtifact returns the artifact the submission must fence against.
//
// For Start the default is the Supervisor's own same-cycle observation, exactly
// what the Console Start fences: `activate_update` replaces the active engine in
// place and persists the new selection in the Supervisor, so the observed
// artifact is the one a restart has to launch. A still-incomplete Manager-owned
// installation overrides it with the artifact its wizard committed, because that
// installation may only ever launch the engine it staged.
//
// Stop carries no artifact: the Supervisor's StopRequest has no such field, and
// terminating the owned child is already artifact-free.
func (r *Reconciler) startArtifact(
	ctx context.Context,
	operationType model.RuntimeOperationType,
	status model.RuntimeObservedStatus,
) (model.RuntimeArtifactID, error) {
	if operationType != model.RuntimeOperationStart {
		return "", nil
	}
	committed, found, err := r.committedArtifact(ctx)
	if err != nil {
		return "", err
	}
	if found {
		return committed, nil
	}
	if status.ActiveGatewayArtifact == nil {
		return "", nil
	}
	return status.ActiveGatewayArtifact.ArtifactID, nil
}

// committedArtifact asks the optional installation authority which engine it
// pinned. Only a Manager-owned installation that has not completed reports one.
func (r *Reconciler) committedArtifact(ctx context.Context) (model.RuntimeArtifactID, bool, error) {
	store, ok := r.desired.(CommittedArtifactStore)
	if !ok {
		return "", false, nil
	}
	artifactID, found, err := store.LoadEmbeddedRuntimeCommittedArtifact(ctx)
	if err != nil {
		return "", false, fmt.Errorf("load embedded Runtime committed artifact: %w", err)
	}
	if !found {
		return "", false, nil
	}
	if !artifactID.IsValid() {
		return "", false, fmt.Errorf("%w: committed artifact %q is not a canonical SHA-256 identity",
			ErrReconcileObservationInvalid, artifactID)
	}
	return artifactID, true, nil
}

func DecideReconcileAction(
	desired model.EmbeddedRuntimeDesiredLifecycle,
	status model.RuntimeObservedStatus,
) (ReconcileAction, error) {
	if !desired.IsValid() {
		return ReconcileActionNone, fmt.Errorf("%w: invalid desired lifecycle %q", ErrReconcileObservationInvalid, desired)
	}
	if err := status.Validate(); err != nil {
		return ReconcileActionNone, fmt.Errorf("%w: %v", ErrReconcileObservationInvalid, err)
	}
	if strings.TrimSpace(string(status.ProtocolVersion)) == "" ||
		strings.TrimSpace(string(status.Identity)) == "" || status.Generation == 0 {
		return ReconcileActionNone, fmt.Errorf("%w: mutation-capable Embedded Runtime metadata is missing", ErrReconcileObservationInvalid)
	}
	if status.State == model.RuntimeStateUnknown {
		return ReconcileActionNone, nil
	}
	if status.Recovery == nil {
		return ReconcileActionNone, fmt.Errorf("%w: Embedded Runtime recovery observation is missing", ErrReconcileObservationInvalid)
	}

	switch desired {
	case model.EmbeddedRuntimeDesiredRunning:
		switch status.State {
		case model.RuntimeStateReady, model.RuntimeStateStarting:
			return ReconcileActionNone, nil
		case model.RuntimeStateOffline:
			if status.Recovery.State != model.RuntimeRecoveryStateInactive {
				return ReconcileActionNone, nil
			}
			if !status.Capabilities.Supports(model.RuntimeCapabilityStart) {
				return ReconcileActionNone, fmt.Errorf("%w: Embedded Runtime does not advertise Start", ErrReconcileObservationInvalid)
			}
			return ReconcileActionStart, nil
		}
	case model.EmbeddedRuntimeDesiredStopped:
		switch status.State {
		case model.RuntimeStateReady, model.RuntimeStateStarting:
			if !status.Capabilities.Supports(model.RuntimeCapabilityStop) {
				return ReconcileActionNone, fmt.Errorf("%w: Embedded Runtime does not advertise Stop", ErrReconcileObservationInvalid)
			}
			return ReconcileActionStop, nil
		case model.RuntimeStateOffline:
			return ReconcileActionNone, nil
		}
	}
	return ReconcileActionNone, fmt.Errorf("%w: unsupported Runtime state %q", ErrReconcileObservationInvalid, status.State)
}

// StableReconcileOperationID derives the Supervisor journal identity of one
// reconcile submission. Everything that decides the intent is in the hash, so a
// repeated cycle resolves to the operation it already created. The artifact is
// included because the Supervisor folds it into the request fingerprint of a
// Start: one operation ID must never map to two fingerprints. attempt is the one
// component that deliberately distinguishes retries of an intent the Supervisor
// recorded as failed.
func StableReconcileOperationID(
	desiredRevision uint64,
	runtimeIdentity model.RuntimeIdentity,
	runtimeGeneration model.RuntimeGeneration,
	action model.RuntimeOperationType,
	expectedArtifact model.RuntimeArtifactID,
	attempt int,
) string {
	hash := sha256.New()
	hash.Write([]byte("runtime-reconcile/v2\x00"))
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], desiredRevision)
	hash.Write(encoded[:])
	identity := []byte(runtimeIdentity)
	binary.BigEndian.PutUint64(encoded[:], uint64(len(identity)))
	hash.Write(encoded[:])
	hash.Write(identity)
	binary.BigEndian.PutUint64(encoded[:], uint64(runtimeGeneration))
	hash.Write(encoded[:])
	actionBytes := []byte(action)
	binary.BigEndian.PutUint64(encoded[:], uint64(len(actionBytes)))
	hash.Write(encoded[:])
	hash.Write(actionBytes)
	artifact := []byte(expectedArtifact)
	binary.BigEndian.PutUint64(encoded[:], uint64(len(artifact)))
	hash.Write(encoded[:])
	hash.Write(artifact)
	binary.BigEndian.PutUint64(encoded[:], uint64(attempt))
	hash.Write(encoded[:])
	return "runtime-reconcile/v2:" + hex.EncodeToString(hash.Sum(nil))
}

func classifyMutationError(err error) error {
	code, ok := ErrorCode(err)
	if !ok {
		return err
	}
	switch code {
	case ProtocolErrorOperationIDConflict:
		return fmt.Errorf("%w: %w", ErrReconcileOperationConflict, err)
	case ProtocolErrorRuntimeIdentityMismatch,
		ProtocolErrorStaleRuntimeGeneration,
		ProtocolErrorOperationStateConflict,
		ProtocolErrorOperationPersistenceUnavailable:
		return fmt.Errorf("%w: %w", ErrReconcileReobserve, err)
	default:
		return err
	}
}
