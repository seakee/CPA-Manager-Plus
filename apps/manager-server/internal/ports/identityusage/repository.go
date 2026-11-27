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

	// ErrStaleObservation means a Collector observation raced with a newer
	// monitoring disable or stopped/superseded Collector run and was rejected.
	ErrStaleObservation = errors.New("stale collector observation")
)

// Binding is the durable attribution window of one Canonical Key. Hashing
// details stay inside the adapter.
type Binding struct {
	APIKeyID            identity.APIKeyID
	FirstSeenAtMS       int64
	Origin              string
	IdentityCreatedAtMS int64
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
	Calls           int64
	Failure         int64
	InputTokens     int64
	OutputTokens    int64
	TotalTokens     int64
	CachedTokens    int64
	ReasoningTokens int64
	LastUsedAtMS    *int64
}

// Bucket is one time-series point. Buckets without events are omitted.
type Bucket struct {
	BucketMS    int64
	Calls       int64
	Failure     int64
	TotalTokens int64
}

// ModelCount summarizes usage calls by model within a window.
type ModelCount struct {
	Model            string   `json:"model"`
	Calls            int64    `json:"calls"`
	TotalTokens      int64    `json:"totalTokens,omitempty"`
	EstimatedCostUSD *float64 `json:"estimatedCostUsd,omitempty"`
}

const RecentStatusSlotCount = 20

// StatusSlot summarizes success and failure counts for one fixed slice of a
// key's summary window.
type StatusSlot struct {
	StartMS int64 `json:"startMs"`
	EndMS   int64 `json:"endMs"`
	Success int64 `json:"success"`
	Failure int64 `json:"failure"`
}

// Window is the aggregated evidence for one key inside a half-open time range.
type Window struct {
	Totals           Totals
	EstimatedCostUSD *float64
	TopModels        []ModelCount
	ModelCount       int
	RecentSlots      []StatusSlot
	// UnmappedEvents counts events of this key's source hash that the
	// projection could not attribute (unknown, ambiguous or stale). They are
	// positive proof that the window is incomplete.
	UnmappedEvents int64
}
// KeyRange bounds one Canonical Key's provable window floor [FromMS, toMS).
// When SlotsFromMS > 0, RecentSlots are sliced across [SlotsFromMS, toMS)
// while event attribution still enforces FromMS.
type KeyRange struct {
	APIKeyID    identity.APIKeyID
	FromMS      int64
	SlotsFromMS int64
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

	// Windows aggregates mapped and unmapped events per key inside each key's
	// half-open [FromMS, toMS) range.
	Windows(ctx context.Context, ranges []KeyRange, toMS int64) (map[identity.APIKeyID]Window, error)

	// Series aggregates mapped events of one key into fixed buckets.
	Series(ctx context.Context, id identity.APIKeyID, fromMS, toMS, bucketMS int64) ([]Bucket, error)

	// RawDeletedIn reports whether archived raw events were deleted inside the
	// range, which bounds what any window can still prove.
	RawDeletedIn(ctx context.Context, fromMS, toMS int64) (bool, error)

	// LatestRawDeletedIn returns the latest deleted raw-event timestamp inside
	// [fromMS, toMS), allowing batched summaries to test each key's own
	// [keyWindowFromMS, toMS) range in one query.
	LatestRawDeletedIn(ctx context.Context, fromMS, toMS int64) (latestTimestampMS int64, anyDeleted bool, err error)

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

	// AdvanceCollectionStarted moves the durable collection start instant forward
	// after an explicit monitoring-disabled gap and atomically clears the gap
	// marker. Unlike MarkCollectionStarted, this updates an earlier timestamp
	// when nowMS is strictly newer.
	AdvanceCollectionStarted(ctx context.Context, nowMS int64) error

	// MonitoringDisabledGap reports whether request monitoring was explicitly
	// disabled since the last proven collection start.
	MonitoringDisabledGap(ctx context.Context) (bool, error)

	// RecordMonitoringDisabledGap durably records that monitoring was disabled
	// so a subsequent start advances collection start forward instead of
	// keeping an earlier start instant.
	RecordMonitoringDisabledGap(ctx context.Context, nowMS int64) error

	// RecordUnreplayedGap durably records that a transport without backlog
	// replay dropped or exceeded its retention window.
	RecordUnreplayedGap(ctx context.Context, nowMS int64) error

	// RecordObservedCollectionStart records a proven Collector observation
	// instant inside an atomic SQLite transaction fenced on disable sequence,
	// run activity, retained-queue continuity, and completion eligibility.
	RecordObservedCollectionStart(ctx context.Context, obs ObservedCollectionStart) error
}

// ObservedCollectionStart describes one real Collector observation or
// retained-queue heartbeat for atomic persistence.
type ObservedCollectionStart struct {
	ObservedAtMS       int64
	RetainedQueue      bool
	Transport          string
	SourceID           string
	RetentionBudgetMS  int64
	ObservedDisableSeq int64
	HasDisableSeq      bool
	InitialRunObserve  bool
	ForceGapAdvance    bool
	StillActive        func() bool
}
