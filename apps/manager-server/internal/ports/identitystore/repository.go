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

	ErrPendingAPIKeyMutation   = errors.New("API-key mutation already pending for runtime")
	ErrPendingCredentialDelete = errors.New("credential delete already pending for physical file")
)

type CredentialDeleteOutcome string

const (
	CredentialDeleteSuccess    CredentialDeleteOutcome = "success"
	CredentialDeleteNotApplied CredentialDeleteOutcome = "not_applied"
	CredentialDeleteUnknown    CredentialDeleteOutcome = "unknown"
)

type PrepareCredentialDeleteParams struct {
	RuntimeIdentity           string
	ObservedRuntimeGeneration uint64
	PhysicalName              string
	SourceAuthIDs             []string
	OwnerInstance             string
	NowMS                     int64
}

type ResolveCredentialDeleteParams struct {
	RuntimeIdentity           string
	ObservedRuntimeGeneration uint64
	ObservedSourceAuthIDs     []string
	IntentID                  string
	NowMS                     int64
}

type CredentialDeleteRepository interface {
	CheckPendingCredentialDelete(ctx context.Context, physicalNames []string, all bool) error
	PrepareCredentialDelete(ctx context.Context, params PrepareCredentialDeleteParams) (string, error)
	MarkCredentialDeleteForwardComplete(ctx context.Context, intentID, ownerInstance string, nowMS int64) error
	ResolveCredentialDelete(ctx context.Context, params ResolveCredentialDeleteParams) (CredentialDeleteOutcome, error)
}

type APIKeyMutationKind string

const (
	APIKeyMutationRotate APIKeyMutationKind = "rotate"
	APIKeyMutationDelete APIKeyMutationKind = "delete"
)

type APIKeyMutationOutcome string

const (
	APIKeyMutationNone       APIKeyMutationOutcome = "none"
	APIKeyMutationSuccess    APIKeyMutationOutcome = "success"
	APIKeyMutationNotApplied APIKeyMutationOutcome = "not_applied"
	APIKeyMutationUnknown    APIKeyMutationOutcome = "unknown"
)

// APIKeyMutationEvidence contains hashes and counts only. Raw keys stay at the
// proxy/CPA transport boundary.
type APIKeyMutationEvidence struct {
	OldHash            string
	NewHash            string
	ExactOldCount      int
	NormalizedOldCount int
	NormalizedNewCount int
}

type PrepareAPIKeyMutationParams struct {
	Kind                      APIKeyMutationKind
	RuntimeIdentity           string
	ObservedRuntimeGeneration uint64
	Evidence                  APIKeyMutationEvidence
	OwnerInstance             string
	NowMS                     int64
}

type ResolveAPIKeyMutationParams struct {
	RuntimeIdentity           string
	ObservedRuntimeGeneration uint64
	ObservedHashes            []string
	NowMS                     int64
	IntentID                  string
}

// MutationRepository is implemented by the same SQLite adapter as Repository.
// It keeps purpose-built mutation methods out of passive-reconciliation fakes.
type MutationRepository interface {
	HasPendingAPIKeyMutation(ctx context.Context, runtimeIdentity string) (bool, error)
	HasAnyPendingAPIKeyMutation(ctx context.Context) (bool, error)
	PrepareAPIKeyMutation(ctx context.Context, params PrepareAPIKeyMutationParams) (string, error)
	MarkAPIKeyMutationForwardComplete(ctx context.Context, intentID, ownerInstance string, nowMS int64) error
	ResolveAPIKeyMutation(ctx context.Context, params ResolveAPIKeyMutationParams) (APIKeyMutationOutcome, error)
}

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

	// FindActiveAPIKeyBySource finds an active APIKeyIdentity and binding for the given runtime identity and normalized API key hash (64-char lowercase hex SHA-256(TrimSpace(raw))).
	FindActiveAPIKeyBySource(ctx context.Context, runtimeIdentity, apiKeyHash string) (identity.APIKeyIdentity, identity.APIKeySourceBinding, error)

	// FindActiveCredentialBySource finds an active CredentialIdentity and binding for the given runtime identity and CPA Auth.ID.
	FindActiveCredentialBySource(ctx context.Context, runtimeIdentity, sourceAuthID string) (identity.CredentialIdentity, identity.CredentialSourceBinding, error)

	// SetAPIKeyLifecycle performs a CAS mutation on an API key's lifecycle fencing on expectedRevision.
	// Monotonically advances updated_at_ms and increments revision if a real transition occurs.
	SetAPIKeyLifecycle(ctx context.Context, id identity.APIKeyID, expectedRevision identity.Revision, nextLifecycle identity.Lifecycle, nowMS int64) (identity.APIKeyIdentity, error)

	// SetCredentialLifecycle performs a CAS mutation on a credential's lifecycle fencing on expectedRevision.
	// Monotonically advances updated_at_ms and increments revision if a real transition occurs.
	SetCredentialLifecycle(ctx context.Context, id identity.CredentialID, expectedRevision identity.Revision, nextLifecycle identity.Lifecycle, nowMS int64) (identity.CredentialIdentity, error)

	// ApplyPassiveSnapshot atomically reconciles APIKey and Credential identities
	// within the given runtimeIdentity scope in a single database transaction.
	// If any error or conflict occurs, the entire transaction is rolled back.
	ApplyPassiveSnapshot(ctx context.Context, params ReconcileSnapshotParams) (ReconcileSnapshotResult, error)
}

// APIKeySnapshotItem represents an observed API key in a reconciliation snapshot.
type APIKeySnapshotItem struct {
	APIKeyHash string // 64-char lowercase hex SHA-256(TrimSpace(raw))
}

// CredentialSnapshotItem represents an observed credential in a reconciliation snapshot.
type CredentialSnapshotItem struct {
	SourceAuthID      string // CPA Auth.ID (non-empty)
	AuthIndex         string
	Provider          string
	PhysicalName      string
	AccountSnapshot   string
	AccountIDSnapshot string
	Disabled          bool
}

// ReconcileSnapshotParams contains the coherent fenced inventory snapshot to apply.
type ReconcileSnapshotParams struct {
	RuntimeIdentity           string
	ObservedRuntimeGeneration uint64
	CaptureStartedAtMS        int64
	ProcessInstanceID         string
	APIKeys                   []APIKeySnapshotItem
	Credentials               []CredentialSnapshotItem
	NowMS                     int64
}

// ReconcileSnapshotResult summarizes the mutations performed during snapshot reconciliation.
type ReconcileSnapshotResult struct {
	APIKeysCreated       int
	APIKeysRefreshed     int
	APIKeysRecovered     int
	APIKeysMissing       int
	CredentialsCreated   int
	CredentialsRefreshed int
	CredentialsRecovered int
	CredentialsMissing   int
}
