// Package identityusage serves the Fresh Key workspace's real, attributable
// usage and its honest coverage. Numbers are only published when Manager can
// prove what they cover; everything else is reported as unknown or partial.
package identityusage

import (
	"context"
	"errors"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityusage"
)

const (
	// DefaultWindowMS is the reporting window of the Key workspace: the last
	// 24 hours, which is also the default bucket span.
	DefaultWindowMS = int64(24 * time.Hour / time.Millisecond)
	// MaxWindowMS bounds an explicit request so one read cannot scan unbounded
	// history.
	MaxWindowMS = int64(31 * 24 * time.Hour / time.Millisecond)
	// maxBucketCount keeps the series small enough for one response.
	maxBucketCount = 24
	minBucketMS    = int64(time.Minute / time.Millisecond)
	maxBucketMS    = int64(time.Hour / time.Millisecond)
)

// ErrWindow is returned for an explicit window Manager refuses to read.
var ErrWindow = errors.New("invalid identity usage window")

// CollectionPhase is the state of the Fresh Collector, which is the only
// writer of usage events in a Fresh installation.
type CollectionPhase string

const (
	PhaseRunning  CollectionPhase = "running"
	PhaseStarting CollectionPhase = "starting"
	PhaseStopped  CollectionPhase = "stopped"
	PhaseError    CollectionPhase = "error"
)

// CollectionProbe reports the live Collector phase without exposing any CPA
// address, credential or error text.
type CollectionProbe func() CollectionPhase

// Coverage explains how far the reported numbers can be trusted.
type Coverage struct {
	State                string `json:"state"`
	Reason               string `json:"reason"`
	CollectingSinceMS    int64  `json:"collectingSinceMs"`
	LastProcessedEventID int64  `json:"lastProcessedEventId"`
	TargetEventID        int64  `json:"targetEventId"`
}

// CoverageState is the compact per-key coverage used by the list summary.
type CoverageState struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

// IncompleteDay is one UTC day whose numbers are a permanent lower bound: a v2
// Upgrade marked it because the 1.x source can never be read again for it.
type IncompleteDay struct {
	Day    string `json:"day"`
	Reason string `json:"reason"`
}

// Totals are published only when the coverage can bound them.
type Totals struct {
	Calls        int64  `json:"calls"`
	Success      int64  `json:"success"`
	Failure      int64  `json:"failure"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	TotalTokens  int64  `json:"totalTokens"`
	LastUsedAtMS *int64 `json:"lastUsedAtMs"`
}

// SeriesPoint is one bucket of the detail series.
type SeriesPoint struct {
	BucketMS    int64 `json:"bucketMs"`
	Calls       int64 `json:"calls"`
	Failure     int64 `json:"failure"`
	TotalTokens int64 `json:"totalTokens"`
}

// DetailResponse is the usage section of one Canonical Key.
type DetailResponse struct {
	KeyID    string        `json:"keyId"`
	FromMS   int64         `json:"fromMs"`
	ToMS     int64         `json:"toMs"`
	BucketMS int64         `json:"bucketMs"`
	Coverage Coverage      `json:"coverage"`
	Totals   *Totals       `json:"totals"`
	Series   []SeriesPoint `json:"series"`
	// IncompleteDays names the UTC days inside the window that a v2 Upgrade
	// marked incomplete. The totals above are then a lower bound, never a
	// measurement of a complete window.
	IncompleteDays []IncompleteDay `json:"incompleteDays,omitempty"`
}

// SummaryItem is one row of the Key list usage column.
type SummaryItem struct {
	KeyID        string        `json:"keyId"`
	Calls        int64         `json:"calls"`
	LastUsedAtMS *int64        `json:"lastUsedAtMs"`
	Coverage     CoverageState `json:"coverage"`
}

// SummaryResponse is the batched usage column of the Key list.
type SummaryResponse struct {
	FromMS   int64         `json:"fromMs"`
	ToMS     int64         `json:"toMs"`
	Coverage CoverageState `json:"coverage"`
	Items    []SummaryItem `json:"items"`
	// IncompleteDays names the UTC days inside the list window that a v2
	// Upgrade marked incomplete, so the summed column is a lower bound.
	IncompleteDays []IncompleteDay `json:"incompleteDays,omitempty"`
}

// Service reads attributable usage for Canonical Keys of the Fresh workspace.
type Service struct {
	repo       ports.Repository
	collection CollectionProbe
	now        func() int64
}

// New builds the usage read service. A nil probe reports an unknown phase, so
// an unwired Collector can never be mistaken for a proven quiet window.
func New(repo ports.Repository, collection CollectionProbe) *Service {
	return &Service{repo: repo, collection: collection, now: func() int64 { return time.Now().UnixMilli() }}
}

// SetClock overrides the clock for deterministic tests.
func (s *Service) SetClock(now func() int64) {
	if now != nil {
		s.now = now
	}
}

// MarkCollectionStart records the first durable moment collection could run.
// It is idempotent: the earliest recorded instant wins.
func (s *Service) MarkCollectionStart(ctx context.Context) error {
	return s.repo.MarkCollectionStarted(ctx, s.now())
}

type evidence struct {
	phase        CollectionPhase
	collectingAt int64
	projection   ports.ProjectionState
	projectionOK bool
	binding      ports.Binding
	bindingErr   error
	window       ports.Window
	rawDeleted   bool
	// incompleteDays are the Upgrade marks overlapping the read range. They are
	// permanent, so they bound the window for the life of the store.
	incompleteDays []ports.IncompleteDay
	// requestedFromMS is the window the caller asked for; windowFromMS is the
	// earliest instant Manager can actually prove. A window that starts before
	// the latter is a lower bound, never a complete observation.
	requestedFromMS int64
	windowFromMS    int64
	windowToMS      int64
}

// evaluate returns the coverage of one key's window. Reasons are stable codes;
// the UI owns their localization.
func evaluate(in evidence) (ports.State, string) {
	switch {
	case in.collectingAt <= 0:
		return ports.StateUnknown, "collection_unproven"
	case errors.Is(in.bindingErr, ports.ErrBindingConflict):
		return ports.StateUnavailable, "binding_conflict"
	case errors.Is(in.bindingErr, ports.ErrBindingNotFound):
		return ports.StateUnavailable, "binding_missing"
	case !in.projectionOK:
		return ports.StateUnavailable, "projection_unavailable"
	case in.phase == PhaseStopped:
		return ports.StateUnavailable, "collection_stopped"
	case in.phase == PhaseError:
		return ports.StateUnavailable, "collection_error"
	case in.phase == PhaseStarting:
		return ports.StateUnknown, "collection_starting"
	case in.projection.Status == "error":
		if in.window.Totals.Calls > 0 {
			return ports.StatePartial, "projection_failed"
		}
		return ports.StateUnknown, "projection_failed"
	case !in.projection.CaughtUp():
		if in.window.Totals.Calls > 0 {
			return ports.StatePartial, "projection_catching_up"
		}
		return ports.StateUnknown, "projection_catching_up"
	case in.windowFromMS >= in.windowToMS:
		// Nothing inside the requested range was ever observed. Publishing a
		// zero here would claim a proven quiet window that does not exist.
		return ports.StateUnknown, "window_unobserved"
	case in.windowFromMS > in.requestedFromMS:
		// Collection or attribution started inside the requested window, so the
		// numbers are a lower bound for it.
		return ports.StatePartial, "window_partial"
	case in.window.UnmappedEvents > 0:
		return ports.StatePartial, "identity_unmapped"
	case len(in.incompleteDays) > 0:
		// A v2 Upgrade marked a day inside the window, so it can never become
		// complete; this outranks the raw-delete evidence, which a later
		// cleanup may remove.
		return ports.StatePartial, "upgrade_incomplete"
	case in.rawDeleted:
		return ports.StatePartial, "raw_deleted"
	}
	return ports.StateComplete, "observed"
}

// Detail reads one key's usage. An explicit window must be bounded; otherwise
// the default window ends now.
func (s *Service) Detail(ctx context.Context, id identity.APIKeyID, fromMS, toMS int64) (DetailResponse, error) {
	if err := id.Validate(); err != nil {
		return DetailResponse{}, err
	}
	nowMS := s.now()
	if fromMS <= 0 && toMS <= 0 {
		fromMS, toMS = nowMS-DefaultWindowMS, nowMS
	}
	if toMS > nowMS {
		toMS = nowMS
	}
	if fromMS <= 0 || toMS <= fromMS || toMS-fromMS > MaxWindowMS {
		return DetailResponse{}, ErrWindow
	}
	in := evidence{phase: s.phase(), requestedFromMS: fromMS, windowFromMS: fromMS, windowToMS: toMS}
	var err error
	if in.collectingAt, err = s.repo.CollectionStartedAtMS(ctx); err != nil {
		return DetailResponse{}, err
	}
	in.binding, in.bindingErr = s.repo.Binding(ctx, id)
	// Manager can only prove a window from the later of the proven collection
	// start and the key's own attribution start.
	if in.collectingAt > in.windowFromMS {
		in.windowFromMS = in.collectingAt
	}
	if in.bindingErr == nil && in.binding.FirstSeenAtMS > in.windowFromMS {
		in.windowFromMS = in.binding.FirstSeenAtMS
	}
	bucketMS := bucketFor(toMS - fromMS)
	responseFromMS := fromMS
	if in.windowFromMS > fromMS && in.windowFromMS < toMS {
		responseFromMS = in.windowFromMS
	}
	response := DetailResponse{KeyID: string(id), FromMS: responseFromMS, ToMS: toMS, BucketMS: bucketMS, Series: []SeriesPoint{}}
	// The projection checkpoint and archive coverage are global facts; they are
	// only meaningful once the key itself is attributable.
	if in.collectingAt > 0 && in.bindingErr == nil {
		in.projection, in.projectionOK = s.projection(ctx)
		if in.windowFromMS < toMS {
			windows, err := s.repo.Windows(ctx, []identity.APIKeyID{id}, in.windowFromMS, toMS)
			if err != nil {
				return DetailResponse{}, err
			}
			in.window = windows[id]
			if in.rawDeleted, err = s.repo.RawDeletedIn(ctx, in.windowFromMS, toMS); err != nil {
				return DetailResponse{}, err
			}
			incompleteDays, err := s.repo.IncompleteDaysIn(ctx, in.windowFromMS, toMS)
			if err != nil {
				return DetailResponse{}, err
			}
			// The mark bounds this window only when it falls inside it: a day
			// before the key existed says nothing about the key's numbers.
			in.incompleteDays = daysWithin(incompleteDays, in.windowFromMS, toMS)
		}
	}
	state, reason := evaluate(in)
	response.Coverage = Coverage{
		State: string(state), Reason: reason, CollectingSinceMS: in.collectingAt,
		LastProcessedEventID: in.projection.LastProcessedEventID, TargetEventID: in.projection.LatestEventID,
	}
	if state == ports.StateComplete || state == ports.StatePartial {
		response.IncompleteDays = incompleteDaysView(in.incompleteDays)
	}
	// Unknown and unavailable windows publish no totals at all: a zero must never
	// stand in for "not measured".
	if state == ports.StateComplete || state == ports.StatePartial {
		totals := Totals{
			Calls: in.window.Totals.Calls, Failure: in.window.Totals.Failure,
			InputTokens: in.window.Totals.InputTokens, OutputTokens: in.window.Totals.OutputTokens,
			TotalTokens: in.window.Totals.TotalTokens, LastUsedAtMS: in.window.Totals.LastUsedAtMS,
		}
		totals.Success = totals.Calls - totals.Failure
		response.Totals = &totals
		if in.windowFromMS < toMS {
			buckets, err := s.repo.Series(ctx, id, in.windowFromMS, toMS, bucketMS)
			if err != nil {
				return DetailResponse{}, err
			}
			for _, bucket := range buckets {
				response.Series = append(response.Series, SeriesPoint{
					BucketMS: bucket.BucketMS, Calls: bucket.Calls,
					Failure: bucket.Failure, TotalTokens: bucket.TotalTokens,
				})
			}
		}
	}
	return response, nil
}

// Summary reads the usage column of the Key list in bounded batches.
func (s *Service) Summary(ctx context.Context, ids []identity.APIKeyID) (SummaryResponse, error) {
	nowMS := s.now()
	fromMS, toMS := nowMS-DefaultWindowMS, nowMS
	response := SummaryResponse{FromMS: fromMS, ToMS: toMS, Items: make([]SummaryItem, 0, len(ids))}

	collectingAt, err := s.repo.CollectionStartedAtMS(ctx)
	if err != nil {
		return SummaryResponse{}, err
	}
	phase := s.phase()
	// The projection checkpoint bounds every key, so it is read even when this
	// workspace has no key yet: the list header must not claim completeness it
	// cannot prove.
	projection, projectionOK := ports.ProjectionState{}, false
	if collectingAt > 0 {
		projection, projectionOK = s.projection(ctx)
	}
	response.Coverage = globalCoverage(phase, collectingAt, projection, projectionOK)

	if len(ids) == 0 {
		return response, nil
	}
	bindings := make(map[identity.APIKeyID]ports.Binding, len(ids))
	bindingErrs := make(map[identity.APIKeyID]error, len(ids))
	attributable := make([]identity.APIKeyID, 0, len(ids))
	for _, id := range ids {
		binding, err := s.repo.Binding(ctx, id)
		bindings[id], bindingErrs[id] = binding, err
		if err == nil {
			attributable = append(attributable, id)
		}
	}
	windows := map[identity.APIKeyID]ports.Window{}
	rawDeleted := false
	var incompleteDays []ports.IncompleteDay
	if len(attributable) > 0 && collectingAt > 0 {
		if windows, err = s.repo.Windows(ctx, attributable, fromMS, toMS); err != nil {
			return SummaryResponse{}, err
		}
		if rawDeleted, err = s.repo.RawDeletedIn(ctx, fromMS, toMS); err != nil {
			return SummaryResponse{}, err
		}
		if incompleteDays, err = s.repo.IncompleteDaysIn(ctx, fromMS, toMS); err != nil {
			return SummaryResponse{}, err
		}
		response.IncompleteDays = incompleteDaysView(incompleteDays)
	}
	items := make([]SummaryItem, 0, len(ids))
	for _, id := range ids {
		item := SummaryItem{KeyID: string(id)}
		if err := bindingErrs[id]; err != nil {
			item.Coverage = CoverageState{State: string(ports.StateUnavailable), Reason: bindingReason(err)}
			items = append(items, item)
			continue
		}
		windowFromMS := fromMS
		if collectingAt > windowFromMS {
			windowFromMS = collectingAt
		}
		if bindings[id].FirstSeenAtMS > windowFromMS {
			windowFromMS = bindings[id].FirstSeenAtMS
		}
		window := windows[id]
		in := evidence{phase: phase, collectingAt: collectingAt, binding: bindings[id],
			window: window, rawDeleted: rawDeleted,
			// Only the days inside this key's provable window bound it: a mark
			// before the key existed says nothing about its numbers.
			incompleteDays: daysWithin(incompleteDays, windowFromMS, toMS), projection: projection,
			projectionOK: projectionOK, requestedFromMS: fromMS,
			windowFromMS: windowFromMS, windowToMS: toMS}
		state, reason := evaluate(in)
		item.Coverage = CoverageState{State: string(state), Reason: reason}
		if state == ports.StateComplete || state == ports.StatePartial {
			item.Calls = window.Totals.Calls
			item.LastUsedAtMS = window.Totals.LastUsedAtMS
		}
		items = append(items, item)
	}
	response.Items = items
	// The list header sums these items, so its coverage is the weakest per-key
	// coverage: one incomplete key makes the total a lower bound or unprovable.
	response.Coverage = aggregateCoverage(response.Coverage, items)
	return response, nil
}

// aggregateCoverage folds per-key coverage into the list header. It never
// upgrades the installation-wide state, and any incomplete key downgrades the
// aggregate: an unknown key hides the sum, and an unavailable one proves it is
// only a lower bound.
func aggregateCoverage(global CoverageState, items []SummaryItem) CoverageState {
	if len(items) == 0 {
		return global
	}
	counts := map[string]int{}
	reasons := map[string]string{}
	for _, item := range items {
		state := item.Coverage.State
		if counts[state] == 0 {
			reasons[state] = item.Coverage.Reason
		}
		counts[state]++
	}
	unknown := counts[string(ports.StateUnknown)]
	partial := counts[string(ports.StatePartial)]
	unavailable := counts[string(ports.StateUnavailable)]
	complete := counts[string(ports.StateComplete)]
	switch {
	case unknown > 0:
		return CoverageState{State: string(ports.StateUnknown), Reason: reasons[string(ports.StateUnknown)]}
	case partial > 0:
		return CoverageState{State: string(ports.StatePartial), Reason: reasons[string(ports.StatePartial)]}
	case unavailable > 0 && complete > 0:
		return CoverageState{State: string(ports.StatePartial), Reason: reasons[string(ports.StateUnavailable)]}
	case unavailable > 0:
		return CoverageState{State: string(ports.StateUnavailable), Reason: reasons[string(ports.StateUnavailable)]}
	}
	return CoverageState{State: string(ports.StateComplete), Reason: "observed"}
}

// globalCoverage reports the installation-wide collection state shown once per
// Key list; it never describes one key's attribution.
func globalCoverage(phase CollectionPhase, collectingAtMS int64, projection ports.ProjectionState, projectionOK bool) CoverageState {
	switch {
	case collectingAtMS <= 0:
		return CoverageState{State: string(ports.StateUnknown), Reason: "collection_unproven"}
	case phase == PhaseStopped:
		return CoverageState{State: string(ports.StateUnavailable), Reason: "collection_stopped"}
	case phase == PhaseError:
		return CoverageState{State: string(ports.StateUnavailable), Reason: "collection_error"}
	case phase == PhaseStarting:
		return CoverageState{State: string(ports.StateUnknown), Reason: "collection_starting"}
	case !projectionOK:
		return CoverageState{State: string(ports.StateUnavailable), Reason: "projection_unavailable"}
	case projection.Status == "error":
		return CoverageState{State: string(ports.StateUnknown), Reason: "projection_failed"}
	case !projection.CaughtUp():
		return CoverageState{State: string(ports.StateUnknown), Reason: "projection_catching_up"}
	}
	return CoverageState{State: string(ports.StateComplete), Reason: "observed"}
}

func (s *Service) phase() CollectionPhase {
	if s.collection == nil {
		return PhaseStarting
	}
	return s.collection()
}

func (s *Service) projection(ctx context.Context) (ports.ProjectionState, bool) {
	state, err := s.repo.ProjectionState(ctx)
	if err != nil {
		return ports.ProjectionState{}, false
	}
	return state, true
}

// daysWithin keeps the marks whose UTC day overlaps [fromMS, toMS). Stored
// days are YYYY-MM-DD, so the comparison is chronological as text.
func daysWithin(days []ports.IncompleteDay, fromMS, toMS int64) []ports.IncompleteDay {
	if len(days) == 0 || toMS <= fromMS {
		return nil
	}
	const dayMS = int64(24 * time.Hour / time.Millisecond)
	from := time.UnixMilli(fromMS / dayMS * dayMS).UTC().Format("2006-01-02")
	to := time.UnixMilli((toMS + dayMS - 1) / dayMS * dayMS).UTC().Format("2006-01-02")
	within := make([]ports.IncompleteDay, 0, len(days))
	for _, day := range days {
		if day.Day >= from && day.Day < to {
			within = append(within, day)
		}
	}
	if len(within) == 0 {
		return nil
	}
	return within
}

// incompleteDaysView converts the storage vocabulary into the response one.
func incompleteDaysView(days []ports.IncompleteDay) []IncompleteDay {
	if len(days) == 0 {
		return nil
	}
	view := make([]IncompleteDay, 0, len(days))
	for _, day := range days {
		view = append(view, IncompleteDay{Day: day.Day, Reason: day.Reason})
	}
	return view
}

func bindingReason(err error) string {
	if errors.Is(err, ports.ErrBindingConflict) {
		return "binding_conflict"
	}
	return "binding_missing"
}

func bucketFor(windowMS int64) int64 {
	bucket := (windowMS + maxBucketCount - 1) / maxBucketCount
	bucket = ((bucket + minBucketMS - 1) / minBucketMS) * minBucketMS
	if bucket < minBucketMS {
		return minBucketMS
	}
	if bucket > maxBucketMS {
		return maxBucketMS
	}
	return bucket
}
