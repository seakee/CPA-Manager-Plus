package identitystore

import (
	"context"
	"errors"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
)

var (
	// ErrNotFound indicates that the requested canonical identity or active source binding does not exist.
	ErrNotFound = errors.New("canonical identity or binding not found")

	// ErrRevisionConflict indicates that the expected business revision did not match the persisted revision during a CAS mutation.
	ErrRevisionConflict = errors.New("canonical identity revision conflict")

	// ErrSourceBindingConflict indicates an active source binding uniqueness conflict within a runtime scope.
	ErrSourceBindingConflict = errors.New("active source binding conflict")

	// ErrInvalidLifecycleTransition indicates that the requested lifecycle transition is forbidden by domain transition rules.
	ErrInvalidLifecycleTransition = errors.New("invalid canonical identity lifecycle transition")

	// ErrRevisionOverflow indicates that the business revision would exceed math.MaxInt64.
	ErrRevisionOverflow = errors.New("canonical identity revision overflow")
)

// Repository is the storage port for canonical identities and source bindings.
type Repository interface {
	// CreateAPIKey atomically persists an APIKeyIdentity and its initial active APIKeySourceBinding in a single transaction.
	CreateAPIKey(ctx context.Context, identity identity.APIKeyIdentity, binding identity.APIKeySourceBinding) error

	// CreateCredential atomically persists a CredentialIdentity and its initial active CredentialSourceBinding in a single transaction.
	CreateCredential(ctx context.Context, identity identity.CredentialIdentity, binding identity.CredentialSourceBinding) error

	// LoadAPIKeyByID loads a canonical APIKeyIdentity by its APIKeyID.
	LoadAPIKeyByID(ctx context.Context, id identity.APIKeyID) (identity.APIKeyIdentity, error)

	// LoadCredentialByID loads a canonical CredentialIdentity by its CredentialID.
	LoadCredentialByID(ctx context.Context, id identity.CredentialID) (identity.CredentialIdentity, error)

	// FindActiveAPIKeyBySource finds an active APIKeyIdentity and binding for the given runtime identity and raw API key SHA-256 hash.
	FindActiveAPIKeyBySource(ctx context.Context, runtimeIdentity, apiKeyHash string) (identity.APIKeyIdentity, identity.APIKeySourceBinding, error)

	// FindActiveCredentialBySource finds an active CredentialIdentity and binding for the given runtime identity and CPA Auth.ID.
	FindActiveCredentialBySource(ctx context.Context, runtimeIdentity, sourceAuthID string) (identity.CredentialIdentity, identity.CredentialSourceBinding, error)

	// SetAPIKeyLifecycle performs a CAS mutation on an API key's lifecycle fencing on expectedRevision.
	// Monotonically advances updated_at_ms and increments revision if a real transition occurs.
	SetAPIKeyLifecycle(ctx context.Context, id identity.APIKeyID, expectedRevision identity.Revision, nextLifecycle identity.Lifecycle, nowMS int64) (identity.APIKeyIdentity, error)

	// SetCredentialLifecycle performs a CAS mutation on a credential's lifecycle fencing on expectedRevision.
	// Monotonically advances updated_at_ms and increments revision if a real transition occurs.
	SetCredentialLifecycle(ctx context.Context, id identity.CredentialID, expectedRevision identity.Revision, nextLifecycle identity.Lifecycle, nowMS int64) (identity.CredentialIdentity, error)
}
