// Package identityusage reads per-Canonical-Key usage that the canonical
// identity projection has already attributed. It never reads or returns a raw
// key, key hash, secret or CPA management credential.
package identityusage

import (
	"context"
	"errors"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
)

// State is the shared coverage vocabulary of the Fresh Key workspace. Only
// StateComplete proves that a window's numbers are exhaustive; every other
// state means Manager cannot prove the numbers are complete.
type State string

const (
	StateComplete    State = "complete"
	StatePartial     State = "partial"
	StateUnknown     State = "unknown"
	StateUnavailable State = "unavailable"
)

var (
	// ErrBindingNotFound means the Canonical Key has no source binding, so no
	// event can ever be attributed to it.
	ErrBindingNotFound = errors.New("canonical key has no source binding")

	// ErrBindingConflict means the key's source hash is actively bound to a
	// different Canonical Key. Manager reports that instead of guessing.
	ErrBindingConflict = errors.New("canonical key source hash is bound to another identity")
)

// Binding is the durable attribution window of one Canonical Key. Hashing
// details stay inside the adapter.
type Binding struct {
	APIKeyID      identity.APIKeyID
	FirstSeenAtMS int64
}

// ProjectionState is the checkpoint of the canonical identity shadow
// projection plus the current usage_events frontier.
type ProjectionState struct {
	Status               string
	LastProcessedEventID int64
	TargetEventID        int64
	LatestEventID        int64
}

// CaughtUp reports whether the projection finished every event Manager can
// already see. Only a ready checkpoint proves that: a pending or backfilling
// projection is not caught up even when its frontier is still empty, because an
// unprocessed window proves nothing about the events it has not read yet.
func (s ProjectionState) CaughtUp() bool {
	if s.Status != "ready" {
		return false
	}
	target := s.TargetEventID
	if s.LatestEventID > target {
		target = s.LatestEventID
	}
	return s.LastProcessedEventID >= target
}

// Totals are window totals for one Canonical Key. Failure counts requests the
// gateway recorded as failed; Success is derived by the caller.
type Totals struct {
	Calls        int64
	Failure      int64
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
	LastUsedAtMS *int64
}

// Bucket is one time-series point. Buckets without events are omitted.
type Bucket struct {
	BucketMS    int64
	Calls       int64
	Failure     int64
	TotalTokens int64
}

// Window is the aggregated evidence for one key inside a half-open time range.
type Window struct {
	Totals Totals
	// UnmappedEvents counts events of this key's source hash that the
	// projection could not attribute (unknown, ambiguous or stale). They are
	// positive proof that the window is incomplete.
	UnmappedEvents int64
}

// IncompleteDay is one UTC day a v2 Upgrade marked incomplete. Reason is a
// stable code; the UI owns its wording.
type IncompleteDay struct {
	Day    string
	Reason string
}

// Repository is the storage port. Every method returns counts only.
type Repository interface {
	// Binding returns the attribution window of the key or ErrBindingNotFound
	// / ErrBindingConflict.
	Binding(ctx context.Context, id identity.APIKeyID) (Binding, error)

	// ProjectionState returns the canonical identity projection checkpoint.
	ProjectionState(ctx context.Context) (ProjectionState, error)

	// Windows aggregates mapped events per key inside [fromMS, toMS).
	Windows(ctx context.Context, ids []identity.APIKeyID, fromMS, toMS int64) (map[identity.APIKeyID]Window, error)

	// Series aggregates mapped events of one key into fixed buckets.
	Series(ctx context.Context, id identity.APIKeyID, fromMS, toMS, bucketMS int64) ([]Bucket, error)

	// RawDeletedIn reports whether archived raw events were deleted inside the
	// range, which bounds what any window can still prove.
	RawDeletedIn(ctx context.Context, fromMS, toMS int64) (bool, error)

	// IncompleteDaysIn returns the UTC days inside the range that a v2 Upgrade
	// permanently marked incomplete. Their numbers are a lower bound forever, so
	// no window may present them as complete.
	IncompleteDaysIn(ctx context.Context, fromMS, toMS int64) ([]IncompleteDay, error)

	// CollectionStartedAtMS returns the durable instant collection first ran for
	// this installation, or 0 when Manager cannot prove it ever ran.
	CollectionStartedAtMS(ctx context.Context) (int64, error)

	// MarkCollectionStarted records that instant. The earliest value wins, so a
	// restart never narrows a proven window.
	MarkCollectionStarted(ctx context.Context, nowMS int64) error
}
