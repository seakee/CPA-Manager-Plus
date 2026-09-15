// Package lifecycle composes Runtime Supervisor journal intent with privileged
// CPA process side effects. It is deliberately small and Supervisor-private.
package lifecycle

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
)

const startOperationType = "start"

var (
	ErrInvalidRequest              = errors.New("invalid start request")
	ErrOperationStateConflict      = errors.New("CPA process state conflicts with start")
	ErrPersistenceUnavailable      = errors.New("operation persistence unavailable")
	ErrProcessStartFailed          = errors.New("CPA process start failed")
	ErrResultPersistenceAfterStart = errors.New("operation result persistence failed after CPA start")
	startRequestFingerprint        = journal.RequestFingerprint(sha256.Sum256([]byte("runtime.start/v1:{}")))
)

// StartRequest is the typed Start operation plus the Runtime Protocol common
// mutation envelope. Executable and argv are intentionally absent.
type StartRequest struct {
	OperationID               string
	ExpectedRuntimeIdentity   string
	ExpectedRuntimeGeneration uint64
}

type journalStore interface {
	ResolveSubmission(context.Context, journal.Authority, journal.Intent) (journal.Operation, bool, error)
	Begin(context.Context, journal.Authority, journal.Intent) (journal.Operation, bool, error)
	MarkRunning(context.Context, string, string) (journal.Operation, error)
	Complete(context.Context, string, string, journal.State, string) (journal.Operation, error)
}

type processManager interface {
	Observe() cpaprocess.Observation
	Start(context.Context, cpaprocess.StartSpec) (cpaprocess.Observation, error)
}

// StartService serializes the first lifecycle mutation. The process manager
// remains the final single-child safety fence.
type StartService struct {
	mu        sync.Mutex
	store     journalStore
	authority journal.Authority
	process   processManager
	spec      cpaprocess.StartSpec
}

func NewStartService(store journalStore, authority journal.Authority, process processManager, spec cpaprocess.StartSpec) (*StartService, error) {
	if store == nil {
		return nil, errors.New("journal store is required")
	}
	if process == nil {
		return nil, errors.New("CPA process manager is required")
	}
	if strings.TrimSpace(authority.RuntimeIdentity) == "" || authority.RuntimeGeneration == 0 {
		return nil, errors.New("valid Runtime authority is required")
	}
	if strings.TrimSpace(spec.Executable) == "" {
		return nil, errors.New("CPA executable is required")
	}
	return &StartService{store: store, authority: authority, process: process, spec: spec}, nil
}

// Start executes one typed Start mutation. Caller cancellation governs request
// work only until durable intent commits. After that point the Supervisor owns
// the short execution sequence and the child lifetime.
func (service *StartService) Start(ctx context.Context, request StartRequest) (journal.Operation, error) {
	if err := ValidateStartRequest(request); err != nil {
		return journal.Operation{}, err
	}

	service.mu.Lock()
	defer service.mu.Unlock()

	intent := journal.Intent{
		OperationID:               request.OperationID,
		OperationType:             startOperationType,
		ExpectedRuntimeIdentity:   request.ExpectedRuntimeIdentity,
		ExpectedRuntimeGeneration: request.ExpectedRuntimeGeneration,
		RequestFingerprint:        startRequestFingerprint,
	}

	existing, found, err := service.store.ResolveSubmission(ctx, service.authority, intent)
	if err != nil {
		return journal.Operation{}, classifyPreIntentJournalError(ctx, err)
	}
	if found {
		return existing, nil
	}

	observation := service.process.Observe()
	switch observation.State {
	case cpaprocess.StateNotStarted, cpaprocess.StateExited:
	case cpaprocess.StateRunning, cpaprocess.StateUnknown:
		return journal.Operation{}, ErrOperationStateConflict
	default:
		return journal.Operation{}, ErrOperationStateConflict
	}

	operation, created, err := service.store.Begin(ctx, service.authority, intent)
	if err != nil {
		return journal.Operation{}, classifyPreIntentJournalError(ctx, err)
	}
	if !created {
		// Another journal writer may have won after ResolveSubmission. Returning
		// the retained operation preserves idempotency and never repeats Start.
		return operation, nil
	}

	// Durable intent now exists. Do not let the HTTP caller own execution or
	// child lifetime from this point forward.
	executionCtx := context.WithoutCancel(ctx)
	operation, err = service.store.MarkRunning(executionCtx, service.authority.RuntimeIdentity, request.OperationID)
	if err != nil {
		return operation, fmt.Errorf("%w: mark operation running: %v", ErrPersistenceUnavailable, err)
	}

	_, startErr := service.process.Start(executionCtx, service.spec)
	if startErr != nil {
		failureCode := "internal_error"
		resultErr := error(ErrProcessStartFailed)
		if errors.Is(startErr, cpaprocess.ErrStateConflict) {
			failureCode = "operation_state_conflict"
			resultErr = ErrOperationStateConflict
		}
		failed, completeErr := service.store.Complete(executionCtx, service.authority.RuntimeIdentity, request.OperationID, journal.StateFailed, failureCode)
		if completeErr != nil {
			return operation, fmt.Errorf("%w: record failed Start result: %v", ErrPersistenceUnavailable, completeErr)
		}
		return failed, resultErr
	}

	succeeded, err := service.store.Complete(executionCtx, service.authority.RuntimeIdentity, request.OperationID, journal.StateSucceeded, "")
	if err != nil {
		return operation, fmt.Errorf("%w: %v", ErrResultPersistenceAfterStart, err)
	}
	return succeeded, nil
}

// ValidateStartRequest performs operation-independent envelope validation before
// capability or execution checks. Start itself repeats this at the application
// boundary so non-HTTP callers cannot bypass validation.
func ValidateStartRequest(request StartRequest) error {
	if request.OperationID == "" {
		return fmt.Errorf("%w: operationId is required", ErrInvalidRequest)
	}
	if !utf8.ValidString(request.OperationID) || len([]byte(request.OperationID)) > 128 {
		return fmt.Errorf("%w: operationId must be valid UTF-8 and at most 128 bytes", ErrInvalidRequest)
	}
	if strings.TrimSpace(request.ExpectedRuntimeIdentity) == "" {
		return fmt.Errorf("%w: expectedRuntimeIdentity is required", ErrInvalidRequest)
	}
	if request.ExpectedRuntimeGeneration == 0 {
		return fmt.Errorf("%w: expectedRuntimeGeneration must be greater than zero", ErrInvalidRequest)
	}
	return nil
}

func classifyPreIntentJournalError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	switch {
	case errors.Is(err, journal.ErrRuntimeIdentityMismatch),
		errors.Is(err, journal.ErrStaleRuntimeGeneration),
		errors.Is(err, journal.ErrOperationIDConflict):
		return err
	default:
		return fmt.Errorf("%w: %v", ErrPersistenceUnavailable, err)
	}
}
