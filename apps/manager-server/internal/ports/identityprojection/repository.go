package identityprojection

import (
	"context"
)

// Repository is the storage port for canonical identity shadow projections and checkpoints.
type Repository interface {
	// CatchUp processes a bounded batch of usage_events, computes shadow mappings,
	// updates the projection table and advances the checkpoint in a single transaction.
	CatchUp(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error)

	// RecordFailure writes a sanitized bounded error into the checkpoint state table.
	RecordFailure(ctx context.Context, err error, nowMS int64) error

	// GetState loads the current checkpoint state.
	GetState(ctx context.Context) (State, error)

	// GetProjectionByEventID loads a projection row by its usage_event_id.
	GetProjectionByEventID(ctx context.Context, eventID int64) (*UsageIdentityProjection, error)

	// GetProjectionByEventHash loads a projection row by its event_hash.
	GetProjectionByEventHash(ctx context.Context, eventHash string) (*UsageIdentityProjection, error)

	// Reset truncates the projection table and resets checkpoint state to 0 for a clean rebuild.
	// Does NOT modify or delete usage_events.
	Reset(ctx context.Context) error
}
