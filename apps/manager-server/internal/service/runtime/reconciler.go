package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

const DefaultReconcileInterval = 5 * time.Second

var (
	ErrReconcileObservationInvalid  = errors.New("Runtime reconcile observation is invalid")
	ErrReconcileOperationConflict   = errors.New("Runtime reconcile operation ID conflict")
	ErrReconcileReobserve           = errors.New("Runtime reconcile must re-observe")
	ErrReconcileOperationIncomplete = errors.New("Runtime reconcile operation is not durably succeeded")
)

type ReconcileAction string

const (
	ReconcileActionNone  ReconcileAction = ""
	ReconcileActionStart ReconcileAction = "start"
	ReconcileActionStop  ReconcileAction = "stop"
)

type DesiredStateStore interface {
	LoadEmbeddedRuntimeDesiredState(context.Context) (model.EmbeddedRuntimeDesiredState, bool, error)
}

type Reconciler struct {
	client   RuntimeClient
	desired  DesiredStateStore
	interval time.Duration
	logf     func(string, ...any)
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
	return &Reconciler{client: client, desired: desired, interval: interval, logf: logf}
}

// Run performs one initial reconcile and then retries at a bounded interval.
// Cancellation only stops this worker; it never translates Manager shutdown
// into a Runtime Stop operation.
func (r *Reconciler) Run(ctx context.Context) {
	if r == nil || r.client == nil || r.desired == nil {
		return
	}
	lastOutcome := ""
	for {
		if ctx.Err() != nil {
			return
		}
		err := r.ReconcileOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		outcome := ""
		if err != nil {
			outcome = err.Error()
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

		timer := time.NewTimer(r.interval)
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
	status, err := r.client.Status(ctx)
	if err != nil {
		return fmt.Errorf("observe embedded Runtime before reconcile: %w", err)
	}
	action, err := DecideReconcileAction(desired.DesiredLifecycle, status)
	if err != nil {
		return err
	}
	if action == ReconcileActionNone {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	operationType := model.RuntimeOperationType(action)
	request := model.RuntimeMutationRequest{
		OperationID:               StableReconcileOperationID(desired.Revision, status.Identity, status.Generation, operationType),
		ExpectedRuntimeIdentity:   status.Identity,
		ExpectedRuntimeGeneration: status.Generation,
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
		return nil
	case model.RuntimeOperationAccepted, model.RuntimeOperationRunning:
		return fmt.Errorf("%w: %s %s is retained in state %s",
			ErrReconcileOperationIncomplete, result.OperationType, result.OperationID, result.State)
	case model.RuntimeOperationFailed:
		failureCode := "unknown"
		if result.Failure != nil && strings.TrimSpace(result.Failure.Code) != "" {
			failureCode = result.Failure.Code
		}
		return fmt.Errorf("%w: %s %s failed with code %s",
			ErrReconcileOperationIncomplete, result.OperationType, result.OperationID, failureCode)
	default:
		return fmt.Errorf("%w: invalid returned operation state %q", ErrReconcileOperationIncomplete, result.State)
	}
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

func StableReconcileOperationID(
	desiredRevision uint64,
	runtimeIdentity model.RuntimeIdentity,
	runtimeGeneration model.RuntimeGeneration,
	action model.RuntimeOperationType,
) string {
	hash := sha256.New()
	hash.Write([]byte("runtime-reconcile/v1\x00"))
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
	return "runtime-reconcile/v1:" + hex.EncodeToString(hash.Sum(nil))
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
