package journal

import (
	"context"
	"errors"
)

// ResolveSubmission validates current Supervisor authority and resolves an
// operation ID without mutating durable state. It exists so operation-specific
// preconditions can be evaluated after idempotency lookup but before Begin
// commits a new intent, preserving Runtime Protocol v1 submission ordering.
func (store *Store) ResolveSubmission(ctx context.Context, authority Authority, intent Intent) (operation Operation, found bool, err error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, false, err
	}
	if err := validateAuthority(authority); err != nil {
		return Operation{}, false, err
	}
	if err := validateIntent(intent); err != nil {
		return Operation{}, false, err
	}
	if intent.ExpectedRuntimeIdentity != authority.RuntimeIdentity {
		return Operation{}, false, ErrRuntimeIdentityMismatch
	}
	if intent.ExpectedRuntimeGeneration != authority.RuntimeGeneration {
		return Operation{}, false, ErrStaleRuntimeGeneration
	}

	operation, err = getOperation(ctx, store.db, authority.RuntimeIdentity, intent.OperationID)
	if errors.Is(err, ErrOperationNotFound) {
		return Operation{}, false, nil
	}
	if err != nil {
		return Operation{}, false, err
	}
	if operation.OperationType != intent.OperationType || operation.RequestFingerprint != intent.RequestFingerprint {
		return Operation{}, false, ErrOperationIDConflict
	}
	return operation, true, nil
}
