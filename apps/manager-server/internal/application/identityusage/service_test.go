package identityusage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	projectionadapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identityprojection"
	sqliteusage "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityusage"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

const (
	testNowMS   = int64(1_760_000_000_000)
	testKeyID   = identity.APIKeyID("a1b2c3d4e5f60718293a4b5c6d7e8f90")
	testOtherID = identity.APIKeyID("0f1e2d3c4b5a69788796a5b4c3d2e1f0")
)

type fakeRepo struct {
	bindings           map[identity.APIKeyID]ports.Binding
	bindingErrs        map[identity.APIKeyID]error
	collectingAt       int64
	projection         ports.ProjectionState
	projectionErr      error
	windows            map[identity.APIKeyID]ports.Window
	series             []ports.Bucket
	rawDeleted         bool
	latestRawDeletedMS *int64
	incomplete         []ports.IncompleteDay
	windowRanges       []ports.KeyRange
	windowFromMS       int64
	windowToMS         int64
	markedAt           int64
}

func (f *fakeRepo) Binding(_ context.Context, id identity.APIKeyID) (ports.Binding, error) {
	if err := f.bindingErrs[id]; err != nil {
		return ports.Binding{}, err
	}
	binding, ok := f.bindings[id]
	if !ok {
		return ports.Binding{}, ports.ErrBindingNotFound
	}
	return binding, nil
}

func (f *fakeRepo) ProjectionState(context.Context) (ports.ProjectionState, error) {
	return f.projection, f.projectionErr
}

func (f *fakeRepo) Windows(_ context.Context, ranges []ports.KeyRange, toMS int64) (map[identity.APIKeyID]ports.Window, error) {
	f.windowRanges = append([]ports.KeyRange(nil), ranges...)
	if len(ranges) > 0 {
		f.windowFromMS = ranges[0].FromMS
	}
	f.windowToMS = toMS
	result := map[identity.APIKeyID]ports.Window{}
	for _, item := range ranges {
		result[item.APIKeyID] = f.windows[item.APIKeyID]
	}
	return result, nil
}

func (f *fakeRepo) Series(context.Context, identity.APIKeyID, int64, int64, int64) ([]ports.Bucket, error) {
	return f.series, nil
}

func (f *fakeRepo) RawDeletedIn(_ context.Context, fromMS, toMS int64) (bool, error) {
	if toMS <= fromMS {
		return false, nil
	}
	if f.latestRawDeletedMS != nil {
		return *f.latestRawDeletedMS >= fromMS && *f.latestRawDeletedMS < toMS, nil
	}
	return f.rawDeleted, nil
}

func (f *fakeRepo) LatestRawDeletedIn(_ context.Context, fromMS, toMS int64) (int64, bool, error) {
	if toMS <= fromMS {
		return 0, false, nil
	}
	if f.latestRawDeletedMS != nil {
		if *f.latestRawDeletedMS >= fromMS && *f.latestRawDeletedMS < toMS {
			return *f.latestRawDeletedMS, true, nil
		}
		return 0, false, nil
	}
	if f.rawDeleted {
		return toMS - 1, true, nil
	}
	return 0, false, nil
}

func (f *fakeRepo) IncompleteDaysIn(context.Context, int64, int64) ([]ports.IncompleteDay, error) {
	return f.incomplete, nil
}

func (f *fakeRepo) CollectionStartedAtMS(context.Context) (int64, error) {
	return f.collectingAt, nil
}

func (f *fakeRepo) MarkCollectionStarted(_ context.Context, nowMS int64) error {
	f.collectingAt, f.markedAt = nowMS, nowMS
	return nil
}

func healthy() *fakeRepo {
	lastUsedAtMS := testNowMS - 1_000_000
	return &fakeRepo{
		bindings: map[identity.APIKeyID]ports.Binding{testKeyID: {
			APIKeyID:            testKeyID,
			FirstSeenAtMS:       1,
			Origin:              "workspace",
			IdentityCreatedAtMS: 1,
		}},
		collectingAt: 1,
		projection:   ports.ProjectionState{Status: "ready", LastProcessedEventID: 4, TargetEventID: 4, LatestEventID: 4},
		windows: map[identity.APIKeyID]ports.Window{testKeyID: {
			Totals: ports.Totals{
				Calls: 3, Failure: 1, InputTokens: 30, OutputTokens: 15, TotalTokens: 45, LastUsedAtMS: &lastUsedAtMS,
			},
			TopModels:  []ports.ModelCount{{Model: "claude-sonnet-4-5", Calls: 3}},
			ModelCount: 1,
			RecentRequests: []ports.RecentRequest{
				{TimestampMS: lastUsedAtMS, Failed: true, StatusCode: 429, Model: "claude-sonnet-4-5"},
			},
		}},
	}
}

func newTestService(repo *fakeRepo, phase CollectionPhase) *Service {
	service := New(repo, func() CollectionPhase { return phase })
	service.SetClock(func() int64 { return testNowMS })
	return service
}

func detail(t *testing.T, repo *fakeRepo, phase CollectionPhase) DetailResponse {
	t.Helper()
	response, err := newTestService(repo, phase).Detail(context.Background(), testKeyID, 0, 0)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	return response
}

func TestDetailPublishesCountsOnlyWhenCoverageProvesThem(t *testing.T) {
	response := detail(t, healthy(), PhaseRunning)
	if response.Coverage.State != "complete" || response.Coverage.Reason != "observed" {
		t.Fatalf("coverage = %+v", response.Coverage)
	}
	if response.Totals == nil || response.Totals.Calls != 3 || response.Totals.Failure != 1 ||
		response.Totals.Success != 2 || response.Totals.TotalTokens != 45 {
		t.Fatalf("totals = %+v", response.Totals)
	}
	if response.Totals.LastUsedAtMS == nil || *response.Totals.LastUsedAtMS != testNowMS-1_000_000 {
		t.Fatalf("last used = %v", response.Totals.LastUsedAtMS)
	}
	if response.BucketMS != 3_600_000 || response.FromMS != testNowMS-DefaultWindowMS || response.ToMS != testNowMS {
		t.Fatalf("window = %+v", response)
	}
}

func TestDetailReportsUnknownAndUnavailableWithoutFabricatingZero(t *testing.T) {
	cases := []struct {
		name   string
		phase  CollectionPhase
		mutate func(*fakeRepo)
		state  string
		reason string
	}{
		{"binding missing", PhaseRunning, func(r *fakeRepo) {
			r.bindings = map[identity.APIKeyID]ports.Binding{}
		}, "unavailable", "binding_missing"},
		{"binding conflict", PhaseRunning, func(r *fakeRepo) {
			r.bindingErrs = map[identity.APIKeyID]error{testKeyID: ports.ErrBindingConflict}
		}, "unavailable", "binding_conflict"},
		{"collection unproven", PhaseRunning, func(r *fakeRepo) { r.collectingAt = 0 }, "unknown", "collection_unproven"},
		{"collector stopped", PhaseStopped, func(*fakeRepo) {}, "unavailable", "collection_stopped"},
		{"collector error", PhaseError, func(*fakeRepo) {}, "unavailable", "collection_error"},
		{"collector starting", PhaseStarting, func(*fakeRepo) {}, "unknown", "collection_starting"},
		{"projection unavailable", PhaseRunning, func(r *fakeRepo) { r.projectionErr = errors.New("missing") }, "unavailable", "projection_unavailable"},
		{"projection error without calls", PhaseRunning, func(r *fakeRepo) {
			r.projection.Status = "error"
			r.windows = map[identity.APIKeyID]ports.Window{}
		}, "unknown", "projection_failed"},
		{"catching up without calls", PhaseRunning, func(r *fakeRepo) {
			r.projection = ports.ProjectionState{Status: "backfilling", LastProcessedEventID: 2, LatestEventID: 4}
			r.windows = map[identity.APIKeyID]ports.Window{}
		}, "unknown", "projection_catching_up"},
		{"pending empty frontier", PhaseRunning, func(r *fakeRepo) {
			r.projection = ports.ProjectionState{Status: "pending"}
			r.windows = map[identity.APIKeyID]ports.Window{}
		}, "unknown", "projection_catching_up"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			repo := healthy()
			if testCase.mutate != nil {
				testCase.mutate(repo)
			}
			response := detail(t, repo, testCase.phase)
			if response.Coverage.State != testCase.state || response.Coverage.Reason != testCase.reason {
				t.Fatalf("coverage = %+v, want %s/%s", response.Coverage, testCase.state, testCase.reason)
			}
			if response.Totals != nil || len(response.Series) != 0 {
				t.Fatalf("unproven window published totals=%+v series=%+v", response.Totals, response.Series)
			}
		})
	}
}

func TestDetailReportsPartialBoundsWithoutClaimingExhaustiveness(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*fakeRepo)
		reason string
	}{
		{"projection catching up", func(r *fakeRepo) {
			r.projection = ports.ProjectionState{Status: "backfilling", LastProcessedEventID: 2, LatestEventID: 4}
		}, "projection_catching_up"},
		{"projection failed", func(r *fakeRepo) { r.projection.Status = "error" }, "projection_failed"},
		{"unattributed events", func(r *fakeRepo) {
			r.windows = map[identity.APIKeyID]ports.Window{testKeyID: {Totals: r.windows[testKeyID].Totals, UnmappedEvents: 2}}
		}, "identity_unmapped"},
		{"raw deleted", func(r *fakeRepo) { r.rawDeleted = true }, "raw_deleted"},
		{"upgrade marked the day", func(r *fakeRepo) {
			r.incomplete = []ports.IncompleteDay{{Day: markedDay(), Reason: "reconcile_mismatch"}}
		}, "upgrade_incomplete"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			repo := healthy()
			testCase.mutate(repo)
			response := detail(t, repo, PhaseRunning)
			if response.Coverage.State != "partial" || response.Coverage.Reason != testCase.reason {
				t.Fatalf("coverage = %+v", response.Coverage)
			}
			if response.Totals == nil || response.Totals.Calls != 3 {
				t.Fatalf("partial window must keep its observed lower bound, totals = %+v", response.Totals)
			}
		})
	}
}

// markedDay is the UTC day one hour inside the default window, so a mark on it
// provably overlaps what the detail read covers.
func markedDay() string {
	return time.UnixMilli(testNowMS - 3_600_000).UTC().Format("2006-01-02")
}

func TestDetailReportsTheUpgradeMarkThatBoundsTheWindow(t *testing.T) {
	repo := healthy()
	repo.incomplete = []ports.IncompleteDay{
		{Day: "1999-12-31", Reason: "projection_lost_before_v1_14_6"},
		{Day: markedDay(), Reason: "reconcile_mismatch"},
	}
	response := detail(t, repo, PhaseRunning)
	if response.Coverage.State != "partial" || response.Coverage.Reason != "upgrade_incomplete" {
		t.Fatalf("coverage = %+v, want the upgrade mark to bound the window", response.Coverage)
	}
	if len(response.IncompleteDays) != 1 || response.IncompleteDays[0].Day != markedDay() ||
		response.IncompleteDays[0].Reason != "reconcile_mismatch" {
		t.Fatalf("incomplete days = %+v, want only the day inside the window", response.IncompleteDays)
	}
	if response.Totals == nil || response.Totals.Calls != 3 {
		t.Fatalf("totals = %+v, want the observed lower bound", response.Totals)
	}

	// A mark that predates the provable window says nothing about this key.
	outside := healthy()
	outside.incomplete = []ports.IncompleteDay{{Day: "1999-12-31", Reason: "reconcile_mismatch"}}
	clean := detail(t, outside, PhaseRunning)
	if clean.Coverage.State != "complete" || len(clean.IncompleteDays) != 0 {
		t.Fatalf("coverage = %+v with %+v, want a complete window", clean.Coverage, clean.IncompleteDays)
	}
}

func TestDetailUsesTheLaterOfCollectionAndAttributionStart(t *testing.T) {
	// The binding is older than the collection start, so the provable window
	// starts when collection actually began. It is still inside the requested
	// window, which therefore becomes a lower bound.
	repo := healthy()
	repo.collectingAt = testNowMS - 3*3_600_000
	response := detail(t, repo, PhaseRunning)
	if response.FromMS != testNowMS-3*3_600_000 {
		t.Fatalf("from = %d, want the collection start", response.FromMS)
	}
	if repo.windowFromMS != response.FromMS || repo.windowToMS != testNowMS {
		t.Fatalf("repo window = [%d,%d)", repo.windowFromMS, repo.windowToMS)
	}
	if response.Coverage.State != "partial" || response.Coverage.Reason != "window_partial" {
		t.Fatalf("coverage = %+v, want a lower bound for the truncated window", response.Coverage)
	}
	if response.Totals == nil || response.Totals.Calls != 3 {
		t.Fatalf("partial window must keep its observed lower bound, totals = %+v", response.Totals)
	}

	// A native workspace key minted after collection was already running did not
	// exist prior to FirstSeenAtMS, so its pre-creation span is provably zero and
	// its coverage is complete while FromMS reflects its attribution start.
	repo = healthy()
	repo.bindings = map[identity.APIKeyID]ports.Binding{testKeyID: {
		APIKeyID:            testKeyID,
		FirstSeenAtMS:       testNowMS - 3_600_000,
		Origin:              "workspace",
		IdentityCreatedAtMS: testNowMS - 3_600_000,
	}}
	response = detail(t, repo, PhaseRunning)
	if response.FromMS != testNowMS-3_600_000 {
		t.Fatalf("from = %d, want the attribution start", response.FromMS)
	}
	if response.Coverage.State != "complete" || response.Coverage.Reason != "observed" {
		t.Fatalf("native workspace key coverage = %+v, want complete/observed", response.Coverage)
	}

	// An adopted key bound inside the requested window already existed on CPA
	// before Manager bound it, so its window remains partial.
	repo = healthy()
	repo.bindings = map[identity.APIKeyID]ports.Binding{testKeyID: {
		APIKeyID:            testKeyID,
		FirstSeenAtMS:       testNowMS - 3_600_000,
		Origin:              "adopted",
		IdentityCreatedAtMS: testNowMS - 3_600_000,
	}}
	response = detail(t, repo, PhaseRunning)
	if response.FromMS != testNowMS-3_600_000 {
		t.Fatalf("from = %d, want the attribution start", response.FromMS)
	}
	if response.Coverage.State != "partial" || response.Coverage.Reason != "window_partial" {
		t.Fatalf("adopted key coverage = %+v, want partial/window_partial", response.Coverage)
	}
}

func TestDetailProvesNothingForWindowsBeforeCoverageStarted(t *testing.T) {
	repo := healthy()
	repo.collectingAt = testNowMS - 3*3_600_000
	repo.bindings = map[identity.APIKeyID]ports.Binding{testKeyID: {APIKeyID: testKeyID, FirstSeenAtMS: testNowMS - 1_000}}

	// The whole requested window predates both collection and attribution. It
	// must not become a complete zero just because there is nothing to sum.
	service := newTestService(repo, PhaseRunning)
	response, err := service.Detail(context.Background(), testKeyID, testNowMS-6*3_600_000, testNowMS-4*3_600_000)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if response.Coverage.State != "unknown" || response.Coverage.Reason != "window_unobserved" {
		t.Fatalf("coverage = %+v, want an unobserved window", response.Coverage)
	}
	if response.Totals != nil || len(response.Series) != 0 {
		t.Fatalf("unobserved window published totals=%+v series=%+v", response.Totals, response.Series)
	}
	if repo.windowFromMS != 0 || repo.windowToMS != 0 {
		t.Fatalf("unobserved window still aggregated [%d,%d)", repo.windowFromMS, repo.windowToMS)
	}
}

func TestDetailRejectsUnboundedWindows(t *testing.T) {
	service := newTestService(healthy(), PhaseRunning)
	for _, window := range [][2]int64{
		{testNowMS, testNowMS - 1},
		{testNowMS - MaxWindowMS - 1, testNowMS},
		{0, testNowMS},
	} {
		if _, err := service.Detail(context.Background(), testKeyID, window[0], window[1]); !errors.Is(err, ErrWindow) {
			t.Fatalf("window %v error = %v, want ErrWindow", window, err)
		}
	}
}

func TestSummaryReportsPerKeyCoverageAndHidesUnprovenCalls(t *testing.T) {
	repo := healthy()
	repo.bindings[testOtherID] = ports.Binding{APIKeyID: testOtherID, FirstSeenAtMS: 1}
	repo.windows[testOtherID] = ports.Window{Totals: ports.Totals{Calls: 7}}
	repo.windowFromMS, repo.windowToMS = 0, 0

	summary, err := newTestService(repo, PhaseRunning).Summary(context.Background(), []identity.APIKeyID{testKeyID, testOtherID})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Coverage.State != "complete" || summary.FromMS != testNowMS-DefaultWindowMS {
		t.Fatalf("global coverage = %+v", summary)
	}
	if len(summary.Items) != 2 {
		t.Fatalf("items = %+v", summary.Items)
	}
	if summary.Items[0].Calls != 3 || summary.Items[0].Success != 2 || summary.Items[0].Failure != 1 ||
		summary.Items[0].TotalTokens != 45 || summary.Items[0].ModelCount != 1 ||
		len(summary.Items[0].TopModels) != 1 || len(summary.Items[0].RecentRequests) != 1 ||
		summary.Items[0].Coverage.State != "complete" ||
		summary.Items[0].LastUsedAtMS == nil || *summary.Items[0].LastUsedAtMS != testNowMS-1_000_000 {
		t.Fatalf("first item = %+v", summary.Items[0])
	}

	// A key with no binding publishes no call count at all.
	unbound := healthy()
	summary, err = newTestService(unbound, PhaseRunning).Summary(context.Background(), []identity.APIKeyID{testOtherID})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Items[0].Coverage.State != "unavailable" || summary.Items[0].Coverage.Reason != "binding_missing" ||
		summary.Items[0].Calls != 0 || summary.Items[0].LastUsedAtMS != nil {
		t.Fatalf("unbound item = %+v", summary.Items[0])
	}

	// A stopped Collector is visible once per list, before any per-key detail.
	summary, err = newTestService(healthy(), PhaseStopped).Summary(context.Background(), []identity.APIKeyID{testKeyID})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Coverage.State != "unavailable" || summary.Coverage.Reason != "collection_stopped" ||
		summary.Items[0].Coverage.State != "unavailable" || summary.Items[0].Calls != 0 {
		t.Fatalf("stopped summary = %+v", summary)
	}
}

func TestSummaryAggregateCoverageReflectsEveryKey(t *testing.T) {
	// The list header sums per-key counts. One key without an attribution window
	// makes that sum a lower bound, never an exact total.
	repo := healthy()
	repo.windows[testOtherID] = ports.Window{Totals: ports.Totals{Calls: 7}}
	summary, err := newTestService(repo, PhaseRunning).Summary(context.Background(), []identity.APIKeyID{testKeyID, testOtherID})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Items[0].Coverage.State != "complete" {
		t.Fatalf("first item = %+v", summary.Items[0])
	}
	if summary.Items[1].Coverage.State != "unavailable" || summary.Items[1].Coverage.Reason != "binding_missing" {
		t.Fatalf("second item = %+v", summary.Items[1])
	}
	if summary.Coverage.State != "partial" || summary.Coverage.Reason != "binding_missing" {
		t.Fatalf("aggregate coverage = %+v, want a lower bound", summary.Coverage)
	}

	// A key whose provable window has not started yet hides the total: the sum
	// cannot bound what was never observed.
	repo = healthy()
	repo.bindings[testOtherID] = ports.Binding{APIKeyID: testOtherID, FirstSeenAtMS: testNowMS}
	repo.windows[testOtherID] = ports.Window{Totals: ports.Totals{Calls: 7}}
	summary, err = newTestService(repo, PhaseRunning).Summary(context.Background(), []identity.APIKeyID{testKeyID, testOtherID})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Items[1].Coverage.State != "unknown" || summary.Items[1].Coverage.Reason != "window_unobserved" {
		t.Fatalf("unobserved item = %+v", summary.Items[1])
	}
	if summary.Coverage.State != "unknown" || summary.Coverage.Reason != "window_unobserved" {
		t.Fatalf("aggregate coverage = %+v, want the weakest key", summary.Coverage)
	}
}

func TestSummaryReportsProjectionStateEvenWithoutKeys(t *testing.T) {
	// An empty workspace must not claim a complete list header while the
	// attribution checkpoint is missing, failing or still behind.
	cases := []struct {
		name   string
		mutate func(*fakeRepo)
		state  string
		reason string
	}{
		{"caught up", func(*fakeRepo) {}, "complete", "observed"},
		{"catching up", func(r *fakeRepo) {
			r.projection = ports.ProjectionState{Status: "backfilling", LastProcessedEventID: 2, LatestEventID: 4}
		}, "unknown", "projection_catching_up"},
		{"pending empty frontier", func(r *fakeRepo) {
			r.projection = ports.ProjectionState{Status: "pending"}
		}, "unknown", "projection_catching_up"},
		{"projection error", func(r *fakeRepo) { r.projection.Status = "error" }, "unknown", "projection_failed"},
		{"projection missing", func(r *fakeRepo) { r.projectionErr = errors.New("missing") }, "unavailable", "projection_unavailable"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			repo := healthy()
			testCase.mutate(repo)
			summary, err := newTestService(repo, PhaseRunning).Summary(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if summary.Coverage.State != testCase.state || summary.Coverage.Reason != testCase.reason {
				t.Fatalf("global coverage = %+v, want %s/%s", summary.Coverage, testCase.state, testCase.reason)
			}
			if len(summary.Items) != 0 {
				t.Fatalf("empty workspace items = %+v", summary.Items)
			}
		})
	}
}

func TestMarkCollectionStartKeepsTheEarliestInstant(t *testing.T) {
	repo := healthy()
	repo.collectingAt = 0
	service := newTestService(repo, PhaseRunning)
	if err := service.MarkCollectionStart(context.Background()); err != nil || repo.markedAt != testNowMS {
		t.Fatalf("mark = %d err = %v", repo.markedAt, err)
	}
}

func TestResponsesCarryNoSecretOrHashField(t *testing.T) {
	repo := healthy()
	service := newTestService(repo, PhaseRunning)
	response, err := service.Detail(context.Background(), testKeyID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	lowered := strings.ToLower(string(encoded))
	for _, forbidden := range []string{"hash", "secret", "managementkey", "ciphertext", "preview"} {
		if strings.Contains(lowered, forbidden) {
			t.Fatalf("usage response leaks %q: %s", forbidden, encoded)
		}
	}
}

// TestDaysWithinKeepsAYearZeroMark reproduces review F4 for the Key workspace:
// the mark writer accepts 0000-12-31, but the calendar clamp started at
// 0001-01-01, so a key window that covered that day reported complete while the
// persisted mark existed.
func TestDaysWithinKeepsAYearZeroMark(t *testing.T) {
	marks := []ports.IncompleteDay{{Day: "0000-12-31", Reason: "reconcile_mismatch"}}
	start := time.Date(0, time.December, 31, 0, 0, 0, 0, time.UTC).UnixMilli()
	if got := daysWithin(marks, start, start+utcDayMilliseconds); len(got) != 1 || got[0].Day != "0000-12-31" {
		t.Fatalf("daysWithin(year zero) = %+v, want the persisted mark", got)
	}
	if got := daysWithin(marks, math.MinInt64, math.MaxInt64); len(got) != 1 {
		t.Fatalf("daysWithin(whole range) = %+v, want the persisted mark", got)
	}
	// The clamp is still a lower bound, not a blanket: a window that starts
	// after year zero does not report the year-zero mark.
	if got := daysWithin(marks, 1, math.MaxInt64); len(got) != 0 {
		t.Fatalf("daysWithin(after year zero) = %+v, want none", got)
	}
}

// TestDaysWithinKeepsMarksWithAnUpperBoundAtTheLimitOfInt64 reproduces review
// L4 for the Key workspace: the same day arithmetic hid every mark from a
// window that ended near the largest int64.
func TestDaysWithinKeepsMarksWithAnUpperBoundAtTheLimitOfInt64(t *testing.T) {
	marks := []ports.IncompleteDay{{Day: "2026-03-01", Reason: "reconcile_mismatch"}}
	for _, upper := range []int64{1 << 42, math.MaxInt64 - utcDayMilliseconds, math.MaxInt64} {
		if got := daysWithin(marks, 1, upper); len(got) != 1 || got[0].Day != "2026-03-01" {
			t.Fatalf("daysWithin(1, %d) = %+v, want the mark", upper, got)
		}
	}
	// The window stays half-open, and a window that starts after the last day
	// the stored form can hold reports nothing.
	start := time.Date(2026, time.March, 2, 0, 0, 0, 0, time.UTC).UnixMilli()
	if got := daysWithin(marks, start, math.MaxInt64); len(got) != 0 {
		t.Fatalf("daysWithin after the mark = %+v, want none", got)
	}
	if got := daysWithin(marks, 1<<60, math.MaxInt64); len(got) != 0 {
		t.Fatalf("daysWithin past the last stored day = %+v, want none", got)
	}
}

func TestSummaryAndDetailAgreeAcrossPreCollectionAndPreBindingEvents(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "summary-detail-parity.sqlite"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqliterepo.Migrate(db); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	ctx := context.Background()

	const (
		runtimeID       = "runtime-parity-1"
		preCollectKeyID = identity.APIKeyID("11111111111111111111111111111111")
		preBindingKeyID = identity.APIKeyID("22222222222222222222222222222222")
		futureKeyID     = identity.APIKeyID("33333333333333333333333333333333")
	)
	fromMS := testNowMS - DefaultWindowMS
	collectingAtMS := testNowMS - 6*3_600_000

	seedKey := func(id identity.APIKeyID, rawSecret, origin string, firstSeenMS int64) string {
		t.Helper()
		sum := sha256.Sum256([]byte(rawSecret))
		hash := hex.EncodeToString(sum[:])
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms) values (?, 1, 'active', ?, ?)`,
			string(id), firstSeenMS, firstSeenMS); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`insert into fresh_client_api_keys (request_id, api_key_id, secret_ciphertext, secret_hash, masked_preview, origin) values (?, ?, 'cipher', ?, 'cpamp_sk_…0000', ?)`,
			hash[:32], string(id), hash, origin); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, first_seen_at_ms, last_seen_at_ms, retired_at_ms) values (?, ?, ?, ?, ?, null)`,
			string(id), runtimeID, hash, firstSeenMS, firstSeenMS); err != nil {
			t.Fatal(err)
		}
		return hash
	}
	seedEvent := func(eventHash string, ts int64, secretHash string, tokens int64) {
		t.Helper()
		if _, err := db.ExecContext(ctx,
			`insert into usage_events (event_hash, timestamp_ms, timestamp, api_key_hash, endpoint, model, input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens, failed, created_at_ms)
			 values (?, ?, 'synthetic', ?, '/v1/responses', 'gpt-5', ?, 0, 0, 0, ?, 0, ?)`,
			eventHash, ts, secretHash, tokens, tokens, ts); err != nil {
			t.Fatal(err)
		}
	}

	// Key 1: native key bound 12h ago (before collectingAt=6h ago). Has 2 pre-collection mapped calls at now-8h
	// and 4 post-collection mapped calls at now-2h.
	hashPreCollect := seedKey(preCollectKeyID, "cpamp_sk_"+strings.Repeat("a", 64), "workspace", testNowMS-12*3_600_000)
	seedEvent("pre-collect-1", testNowMS-8*3_600_000, hashPreCollect, 10)
	seedEvent("pre-collect-2", testNowMS-8*3_600_000+1000, hashPreCollect, 10)
	for i := range 4 {
		seedEvent("post-collect-"+string(rune('a'+i)), testNowMS-2*3_600_000+int64(i*1000), hashPreCollect, 15)
	}

	// Key 2: adopted key bound 2h ago (after collectingAt=6h ago). Has 3 pre-binding events at now-4h (marked stale)
	// and 5 post-binding mapped calls at now-1h.
	hashPreBinding := seedKey(preBindingKeyID, "cpamp_sk_"+strings.Repeat("b", 64), "adopted", testNowMS-2*3_600_000)
	for i := range 3 {
		seedEvent("pre-bind-"+string(rune('a'+i)), testNowMS-4*3_600_000+int64(i*1000), hashPreBinding, 20)
	}
	for i := range 5 {
		seedEvent("post-bind-"+string(rune('a'+i)), testNowMS-3_600_000+int64(i*1000), hashPreBinding, 25)
	}

	// Key 3: adopted key bound at testNowMS (windowFromMS == toMS -> unobserved window), with a pre-binding event at fromMS+1000.
	hashFuture := seedKey(futureKeyID, "cpamp_sk_"+strings.Repeat("c", 64), "adopted", testNowMS)
	seedEvent("pre-future-1", fromMS+1000, hashFuture, 30)
	projRepo := projectionadapter.New(db)
	if res, err := projRepo.CatchUp(ctx, 100, testNowMS); err != nil || res.Pending {
		t.Fatalf("CatchUp = %+v (%v)", res, err)
	}

	repo := sqliteusage.New(db)
	if err := repo.MarkCollectionStarted(ctx, collectingAtMS); err != nil {
		t.Fatal(err)
	}
	svc := New(repo, func() CollectionPhase { return PhaseRunning })
	svc.SetClock(func() int64 { return testNowMS })

	ids := []identity.APIKeyID{preCollectKeyID, preBindingKeyID, futureKeyID}
	summary, err := svc.Summary(ctx, ids)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if len(summary.Items) != len(ids) {
		t.Fatalf("Summary items = %d, want %d", len(summary.Items), len(ids))
	}

	for i, id := range ids {
		detail, err := svc.Detail(ctx, id, 0, 0)
		if err != nil {
			t.Fatalf("Detail(%s): %v", id, err)
		}
		item := summary.Items[i]
		if item.Coverage.State != detail.Coverage.State || item.Coverage.Reason != detail.Coverage.Reason {
			t.Fatalf("key %s coverage mismatch: Summary=%+v Detail=%+v", id, item.Coverage, detail.Coverage)
		}
		var detailCalls int64
		var detailLastUsed *int64
		if detail.Totals != nil {
			detailCalls = detail.Totals.Calls
			detailLastUsed = detail.Totals.LastUsedAtMS
		}
		if item.Calls != detailCalls {
			t.Fatalf("key %s calls mismatch: Summary=%d Detail=%d", id, item.Calls, detailCalls)
		}
		if (item.LastUsedAtMS == nil) != (detailLastUsed == nil) ||
			(item.LastUsedAtMS != nil && *item.LastUsedAtMS != *detailLastUsed) {
			t.Fatalf("key %s lastUsed mismatch: Summary=%v Detail=%v", id, item.LastUsedAtMS, detailLastUsed)
		}
	}

	// Explicit expectations: pre-collection and pre-binding events are excluded from published counts.
	if summary.Items[0].Calls != 4 || summary.Items[0].Coverage.State != "partial" || summary.Items[0].Coverage.Reason != "window_partial" {
		t.Fatalf("preCollectKey summary item = %+v, want calls=4 partial/window_partial", summary.Items[0])
	}
	if summary.Items[1].Calls != 5 || summary.Items[1].Coverage.State != "partial" || summary.Items[1].Coverage.Reason != "window_partial" {
		t.Fatalf("preBindingKey summary item = %+v, want calls=5 partial/window_partial", summary.Items[1])
	}
	if summary.Items[2].Calls != 0 || summary.Items[2].Coverage.State != "unknown" || summary.Items[2].Coverage.Reason != "window_unobserved" {
		t.Fatalf("futureKey summary item = %+v, want calls=0 unknown/window_unobserved", summary.Items[2])
	}

	// Also verify catching-up parity when a key has ONLY pre-collection calls: neither Summary nor Detail may
	// leak pre-collection calls into a partial lower bound while projection is catching up.
	preOnlyKeyID := identity.APIKeyID("44444444444444444444444444444444")
	hashPreOnly := seedKey(preOnlyKeyID, "cpamp_sk_"+strings.Repeat("d", 64), "workspace", testNowMS-12*3_600_000)
	seedEvent("pre-only-1", testNowMS-9*3_600_000, hashPreOnly, 10)
	if res, err := projRepo.CatchUp(ctx, 100, testNowMS); err != nil || res.Pending {
		t.Fatalf("CatchUp(preOnly) = %+v (%v)", res, err)
	}
	if _, err := db.ExecContext(ctx,
		`update gateway_usage_identity_projection_state set status = 'backfilling' where state_name = ?`,
		projectionadapter.StateName); err != nil {
		t.Fatal(err)
	}
	sumCatchUp, err := svc.Summary(ctx, []identity.APIKeyID{preOnlyKeyID})
	if err != nil {
		t.Fatal(err)
	}
	detCatchUp, err := svc.Detail(ctx, preOnlyKeyID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if sumCatchUp.Items[0].Coverage.State != detCatchUp.Coverage.State || sumCatchUp.Items[0].Coverage.Reason != detCatchUp.Coverage.Reason || sumCatchUp.Items[0].Coverage.State != "unknown" ||
		sumCatchUp.Items[0].Coverage.Reason != "projection_catching_up" || sumCatchUp.Items[0].Calls != 0 {
		t.Fatalf("catching-up pre-collection parity: Summary=%+v Detail=%+v", sumCatchUp.Items[0], detCatchUp)
	}
}

func TestNativeKeysCreatedInsideWindowAndAtReadInstantReportCompleteCoverage(t *testing.T) {
	const (
		workspaceKeyID = identity.APIKeyID("55555555555555555555555555555555")
		installerKeyID = identity.APIKeyID("66666666666666666666666666666666")
		immediateKeyID = identity.APIKeyID("77777777777777777777777777777777")
		adoptedKeyID   = identity.APIKeyID("88888888888888888888888888888888")
	)

	// 1. Native workspace + installer keys created < 24h ago (plus same-millisecond immediate read)
	// with 0 calls report StateComplete, Calls: 0, and workspace aggregate StateComplete.
	repo := &fakeRepo{
		collectingAt: testNowMS - 12*3_600_000,
		projection:   ports.ProjectionState{Status: "ready", LastProcessedEventID: 0, TargetEventID: 0, LatestEventID: 0},
		bindings: map[identity.APIKeyID]ports.Binding{
			workspaceKeyID: {
				APIKeyID:            workspaceKeyID,
				FirstSeenAtMS:       testNowMS - 3_600_000,
				Origin:              "workspace",
				IdentityCreatedAtMS: testNowMS - 3_600_000,
			},
			installerKeyID: {
				APIKeyID:            installerKeyID,
				FirstSeenAtMS:       testNowMS - 1_800_000,
				Origin:              "installer",
				IdentityCreatedAtMS: testNowMS - 1_800_000,
			},
			immediateKeyID: {
				APIKeyID:            immediateKeyID,
				FirstSeenAtMS:       testNowMS,
				Origin:              "workspace",
				IdentityCreatedAtMS: testNowMS,
			},
		},
		windows: map[identity.APIKeyID]ports.Window{},
	}
	svc := newTestService(repo, PhaseRunning)
	ctx := context.Background()

	ids := []identity.APIKeyID{workspaceKeyID, installerKeyID, immediateKeyID}
	summary, err := svc.Summary(ctx, ids)
	if err != nil {
		t.Fatalf("Summary(native keys): %v", err)
	}
	if summary.Coverage.State != "complete" || summary.Coverage.Reason != "observed" {
		t.Fatalf("native workspace aggregate coverage = %+v, want complete/observed", summary.Coverage)
	}
	for i, id := range ids {
		item := summary.Items[i]
		if item.Coverage.State != "complete" || item.Coverage.Reason != "observed" || item.Calls != 0 || item.LastUsedAtMS != nil {
			t.Fatalf("native summary item[%s] = %+v, want complete/observed with 0 calls", id, item)
		}
		det, err := svc.Detail(ctx, id, 0, 0)
		if err != nil {
			t.Fatalf("Detail(%s): %v", id, err)
		}
		if det.Coverage.State != "complete" || det.Coverage.Reason != "observed" {
			t.Fatalf("native detail coverage[%s] = %+v, want complete/observed", id, det.Coverage)
		}
		if det.Totals == nil || det.Totals.Calls != 0 || det.Totals.Success != 0 || det.Totals.Failure != 0 || det.Totals.LastUsedAtMS != nil {
			t.Fatalf("native detail totals[%s] = %+v, want zeroed Totals{Calls:0}", id, det.Totals)
		}
	}

	// 1b. Sub-millisecond after install completion (collectingAt == FirstSeenAtMS == testNowMS-1 < toMS)
	// reports StateComplete, whereas a window ending at the exact collection instant (toMS == collectingAt)
	// contains zero milliseconds of collection and reports StateUnknown ("window_unobserved").
	justStartedRepo := &fakeRepo{
		collectingAt: testNowMS - 1,
		projection:   ports.ProjectionState{Status: "ready"},
		bindings: map[identity.APIKeyID]ports.Binding{
			installerKeyID: {
				APIKeyID:            installerKeyID,
				FirstSeenAtMS:       testNowMS - 1,
				Origin:              "installer",
				IdentityCreatedAtMS: testNowMS - 1,
			},
		},
		windows: map[identity.APIKeyID]ports.Window{},
	}
	justStartedSvc := newTestService(justStartedRepo, PhaseRunning)
	justStartedSum, err := justStartedSvc.Summary(ctx, []identity.APIKeyID{installerKeyID})
	if err != nil {
		t.Fatalf("Summary(just-started installer): %v", err)
	}
	if justStartedSum.Coverage.State != "complete" || justStartedSum.Items[0].Coverage.State != "complete" || justStartedSum.Items[0].Calls != 0 {
		t.Fatalf("just-started installer summary = %+v", justStartedSum)
	}
	zeroLenDet, err := justStartedSvc.Detail(ctx, installerKeyID, testNowMS-3_600_000, testNowMS-1)
	if err != nil {
		t.Fatalf("Detail(toMS == collectingAt): %v", err)
	}
	if zeroLenDet.Coverage.State != "unknown" || zeroLenDet.Coverage.Reason != "window_unobserved" || zeroLenDet.Totals != nil {
		t.Fatalf("toMS == collectingAt detail = %+v, want unknown/window_unobserved", zeroLenDet)
	}
	// 2. Adopted key bound < 24h ago still reports StatePartial (window_partial).
	repo.bindings[adoptedKeyID] = ports.Binding{
		APIKeyID:            adoptedKeyID,
		FirstSeenAtMS:       testNowMS - 3_600_000,
		Origin:              "adopted",
		IdentityCreatedAtMS: testNowMS - 3_600_000,
	}
	adoptedDet, err := svc.Detail(ctx, adoptedKeyID, 0, 0)
	if err != nil {
		t.Fatalf("Detail(adopted): %v", err)
	}
	if adoptedDet.Coverage.State != "partial" || adoptedDet.Coverage.Reason != "window_partial" {
		t.Fatalf("adopted detail coverage = %+v, want partial/window_partial", adoptedDet.Coverage)
	}
	mixedSummary, err := svc.Summary(ctx, []identity.APIKeyID{workspaceKeyID, adoptedKeyID})
	if err != nil {
		t.Fatalf("Summary(workspace+adopted): %v", err)
	}
	if mixedSummary.Items[0].Coverage.State != "complete" ||
		mixedSummary.Items[1].Coverage.State != "partial" || mixedSummary.Items[1].Coverage.Reason != "window_partial" ||
		mixedSummary.Coverage.State != "partial" || mixedSummary.Coverage.Reason != "window_partial" {
		t.Fatalf("mixed workspace+adopted summary = %+v", mixedSummary)
	}

	// 3. Native key when collectingAt > FirstSeenAtMS still reports StatePartial (window_partial).
	lateCollectorRepo := &fakeRepo{
		collectingAt: testNowMS - 2*3_600_000,
		projection:   ports.ProjectionState{Status: "ready"},
		bindings: map[identity.APIKeyID]ports.Binding{
			workspaceKeyID: {
				APIKeyID:            workspaceKeyID,
				FirstSeenAtMS:       testNowMS - 6*3_600_000,
				Origin:              "workspace",
				IdentityCreatedAtMS: testNowMS - 6*3_600_000,
			},
		},
		windows: map[identity.APIKeyID]ports.Window{},
	}
	lateSvc := newTestService(lateCollectorRepo, PhaseRunning)
	lateDet, err := lateSvc.Detail(ctx, workspaceKeyID, 0, 0)
	if err != nil {
		t.Fatalf("Detail(late collector): %v", err)
	}
	if lateDet.Coverage.State != "partial" || lateDet.Coverage.Reason != "window_partial" {
		t.Fatalf("late collector native detail coverage = %+v, want partial/window_partial", lateDet.Coverage)
	}
	lateSum, err := lateSvc.Summary(ctx, []identity.APIKeyID{workspaceKeyID})
	if err != nil {
		t.Fatalf("Summary(late collector): %v", err)
	}
	if lateSum.Items[0].Coverage.State != "partial" || lateSum.Items[0].Coverage.Reason != "window_partial" ||
		lateSum.Coverage.State != "partial" || lateSum.Coverage.Reason != "window_partial" {
		t.Fatalf("late collector native summary = %+v, want partial/window_partial", lateSum)
	}

	// 4. Late-enabled collector on an installer key (monitoring disabled during setup and enabled hours later:
	// FirstSeenAtMS < collectingAt) must NOT overclaim complete coverage — it reports StatePartial ("window_partial"),
	// whereas MarkCollectionStartAt(FirstSeenAtMS) at synchronous setup completion sets collectingAt <= FirstSeenAtMS
	// and reports StateComplete.
	lateCollectorRepo.bindings[installerKeyID] = ports.Binding{
		APIKeyID:            installerKeyID,
		FirstSeenAtMS:       testNowMS - 6*3_600_000,
		Origin:              "installer",
		IdentityCreatedAtMS: testNowMS - 6*3_600_000,
	}
	instLateDet, err := lateSvc.Detail(ctx, installerKeyID, 0, 0)
	if err != nil {
		t.Fatalf("Detail(installer late collector): %v", err)
	}
	if instLateDet.Coverage.State != "partial" || instLateDet.Coverage.Reason != "window_partial" {
		t.Fatalf("installer late collector detail = %+v, want partial/window_partial", instLateDet)
	}
	if err := lateSvc.MarkCollectionStartAt(ctx, testNowMS-6*3_600_000); err != nil {
		t.Fatalf("MarkCollectionStartAt: %v", err)
	}
	lateCollectorRepo.collectingAt = testNowMS - 6*3_600_000
	instCompleteDet, err := lateSvc.Detail(ctx, installerKeyID, 0, 0)
	if err != nil {
		t.Fatalf("Detail(installer completion floor): %v", err)
	}
	if instCompleteDet.Coverage.State != "complete" || instCompleteDet.Coverage.Reason != "observed" {
		t.Fatalf("installer completion-floor detail = %+v, want complete/observed", instCompleteDet)
	}
}

func TestSummaryScopesRawDeletedPerKeyMatchingDetail(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "raw-deleted-per-key.sqlite"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqliterepo.Migrate(db); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	ctx := context.Background()

	const (
		runtimeID  = "runtime-raw-delete-1"
		oldKeyID   = identity.APIKeyID("99999999999999999999999999999991")
		newerKeyID = identity.APIKeyID("99999999999999999999999999999992")
	)
	collectingAtMS := testNowMS - 72*3_600_000
	oldCreatedAtMS := testNowMS - 48*3_600_000
	newerCreatedAtMS := testNowMS - 2*3_600_000

	seedNativeKey := func(id identity.APIKeyID, secret string, createdMS int64) {
		t.Helper()
		sum := sha256.Sum256([]byte(secret))
		hash := hex.EncodeToString(sum[:])
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms) values (?, 1, 'active', ?, ?)`,
			string(id), createdMS, createdMS); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`insert into fresh_client_api_keys (request_id, api_key_id, secret_ciphertext, secret_hash, masked_preview, origin) values (?, ?, 'cipher', ?, 'cpamp_sk_…0000', 'workspace')`,
			hash[:32], string(id), hash); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, first_seen_at_ms, last_seen_at_ms, retired_at_ms) values (?, ?, ?, ?, ?, null)`,
			string(id), runtimeID, hash, createdMS, createdMS); err != nil {
			t.Fatal(err)
		}
	}
	seedNativeKey(oldKeyID, "cpamp_sk_"+strings.Repeat("1", 64), oldCreatedAtMS)
	seedNativeKey(newerKeyID, "cpamp_sk_"+strings.Repeat("2", 64), newerCreatedAtMS)

	projRepo := projectionadapter.New(db)
	if res, err := projRepo.CatchUp(ctx, 100, testNowMS); err != nil || res.Pending {
		t.Fatalf("CatchUp = %+v (%v)", res, err)
	}

	// Archive deletion occurred 20h ago: overlaps oldKeyID's 24h window [now-24h, now),
	// while predating newerKeyID's creation at now-2h.
	deletedMinMS := testNowMS - 22*3_600_000
	deletedMaxMS := testNowMS - 20*3_600_000
	if _, err := db.ExecContext(ctx,
		`insert into usage_archive_deleted_coverage_daily (utc_day, deleted_event_count, min_timestamp_ms, max_timestamp_ms) values (?, 3, ?, ?)`,
		deletedMinMS/utcDayMilliseconds, deletedMinMS, deletedMaxMS); err != nil {
		t.Fatal(err)
	}

	repo := sqliteusage.New(db)
	if err := repo.MarkCollectionStarted(ctx, collectingAtMS); err != nil {
		t.Fatal(err)
	}
	svc := New(repo, func() CollectionPhase { return PhaseRunning })
	svc.SetClock(func() int64 { return testNowMS })

	summary, err := svc.Summary(ctx, []identity.APIKeyID{oldKeyID, newerKeyID})
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	oldDetail, err := svc.Detail(ctx, oldKeyID, 0, 0)
	if err != nil {
		t.Fatalf("Detail(oldKey): %v", err)
	}
	newerDetail, err := svc.Detail(ctx, newerKeyID, 0, 0)
	if err != nil {
		t.Fatalf("Detail(newerKey): %v", err)
	}

	if summary.Items[0].Coverage.State != "partial" || summary.Items[0].Coverage.Reason != "raw_deleted" ||
		oldDetail.Coverage.State != "partial" || oldDetail.Coverage.Reason != "raw_deleted" {
		t.Fatalf("old key rawDeleted mismatch: Summary=%+v Detail=%+v", summary.Items[0].Coverage, oldDetail.Coverage)
	}
	if summary.Items[1].Coverage.State != "complete" || summary.Items[1].Coverage.Reason != "observed" ||
		newerDetail.Coverage.State != "complete" || newerDetail.Coverage.Reason != "observed" {
		t.Fatalf("newer key rawDeleted mismatch: Summary=%+v Detail=%+v, want complete/observed", summary.Items[1].Coverage, newerDetail.Coverage)
	}
	if summary.Coverage.State != "partial" || summary.Coverage.Reason != "raw_deleted" {
		t.Fatalf("combined summary coverage = %+v, want partial/raw_deleted", summary.Coverage)
	}

	newerOnlySummary, err := svc.Summary(ctx, []identity.APIKeyID{newerKeyID})
	if err != nil {
		t.Fatalf("Summary(newerKey only): %v", err)
	}
	if newerOnlySummary.Coverage.State != "complete" || newerOnlySummary.Coverage.Reason != "observed" {
		t.Fatalf("newer-only aggregate coverage = %+v, want complete/observed", newerOnlySummary.Coverage)
	}
}

func TestSummaryAndDetailPreserveLifetimeLastUsedAcross24hAndRotatedBindings(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "lifetime-last-used.sqlite"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqliterepo.Migrate(db); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	ctx := context.Background()

	const (
		runtimeID            = "runtime-lifetime-1"
		rotatedIdle25hKeyID  = identity.APIKeyID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1")
		rotatedFreshNewKeyID = identity.APIKeyID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa2")
	)
	createdAtMS := testNowMS - 72*3_600_000
	collectingAtMS := createdAtMS

	hashFor := func(secret string) string {
		sum := sha256.Sum256([]byte(secret))
		return hex.EncodeToString(sum[:])
	}
	seedEvent := func(eventHash string, ts int64, secretHash string) {
		t.Helper()
		if _, err := db.ExecContext(ctx,
			`insert into usage_events (event_hash, timestamp_ms, timestamp, api_key_hash, endpoint, model, input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens, failed, created_at_ms)
			 values (?, ?, 'synthetic', ?, '/v1/responses', 'gpt-5', 10, 5, 0, 0, 15, 0, ?)`,
			eventHash, ts, secretHash, ts); err != nil {
			t.Fatal(err)
		}
	}

	// Key 1: created 72h ago, used 30h ago under S1, rotated 26h ago to S2, used 25h ago under S2, 0 calls in last 24h.
	hash1Old := hashFor("cpamp_sk_" + strings.Repeat("e", 64))
	hash1New := hashFor("cpamp_sk_" + strings.Repeat("f", 64))
	rot1AtMS := testNowMS - 26*3_600_000
	call1OldMS := testNowMS - 30*3_600_000
	call1NewMS := testNowMS - 25*3_600_000
	if _, err := db.ExecContext(ctx,
		`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms) values (?, 2, 'active', ?, ?)`,
		string(rotatedIdle25hKeyID), createdAtMS, rot1AtMS); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`insert into fresh_client_api_keys (request_id, api_key_id, secret_ciphertext, secret_hash, masked_preview, origin) values (?, ?, 'cipher', ?, 'cpamp_sk_…0001', 'workspace')`,
		hash1New[:32], string(rotatedIdle25hKeyID), hash1New); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, first_seen_at_ms, last_seen_at_ms, retired_at_ms) values (?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, null)`,
		string(rotatedIdle25hKeyID), runtimeID, hash1Old, createdAtMS, rot1AtMS, rot1AtMS,
		string(rotatedIdle25hKeyID), runtimeID, hash1New, rot1AtMS, rot1AtMS); err != nil {
		t.Fatal(err)
	}
	seedEvent("rot1-old", call1OldMS, hash1Old)
	seedEvent("rot1-new", call1NewMS, hash1New)

	// Key 2: created 72h ago, used 40h ago under S1, rotated 1h ago to S2 with 0 calls since rotation.
	hash2Old := hashFor("cpamp_sk_" + strings.Repeat("3", 64))
	hash2New := hashFor("cpamp_sk_" + strings.Repeat("4", 64))
	rot2AtMS := testNowMS - 3_600_000
	call2OldMS := testNowMS - 40*3_600_000
	if _, err := db.ExecContext(ctx,
		`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms) values (?, 2, 'active', ?, ?)`,
		string(rotatedFreshNewKeyID), createdAtMS, rot2AtMS); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`insert into fresh_client_api_keys (request_id, api_key_id, secret_ciphertext, secret_hash, masked_preview, origin) values (?, ?, 'cipher', ?, 'cpamp_sk_…0002', 'workspace')`,
		hash2New[:32], string(rotatedFreshNewKeyID), hash2New); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, first_seen_at_ms, last_seen_at_ms, retired_at_ms) values (?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, null)`,
		string(rotatedFreshNewKeyID), runtimeID, hash2Old, createdAtMS, rot2AtMS, rot2AtMS,
		string(rotatedFreshNewKeyID), runtimeID, hash2New, rot2AtMS, rot2AtMS); err != nil {
		t.Fatal(err)
	}
	seedEvent("rot2-old", call2OldMS, hash2Old)

	projRepo := projectionadapter.New(db)
	if res, err := projRepo.CatchUp(ctx, 100, testNowMS); err != nil || res.Pending {
		t.Fatalf("CatchUp = %+v (%v)", res, err)
	}

	repo := sqliteusage.New(db)
	if err := repo.MarkCollectionStarted(ctx, collectingAtMS); err != nil {
		t.Fatal(err)
	}
	svc := New(repo, func() CollectionPhase { return PhaseRunning })
	svc.SetClock(func() int64 { return testNowMS })

	summary, err := svc.Summary(ctx, []identity.APIKeyID{rotatedIdle25hKeyID, rotatedFreshNewKeyID})
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if summary.Coverage.State != "complete" || summary.Coverage.Reason != "observed" {
		t.Fatalf("Summary coverage = %+v, want complete/observed", summary.Coverage)
	}

	wantLastUsed := []int64{call1NewMS, call2OldMS}
	for i, id := range []identity.APIKeyID{rotatedIdle25hKeyID, rotatedFreshNewKeyID} {
		item := summary.Items[i]
		if item.Coverage.State != "complete" || item.Calls != 0 || item.LastUsedAtMS == nil || *item.LastUsedAtMS != wantLastUsed[i] {
			t.Fatalf("Summary[%s] = %+v, want complete calls=0 lastUsed=%d", id, item, wantLastUsed[i])
		}
		det, err := svc.Detail(ctx, id, 0, 0)
		if err != nil {
			t.Fatalf("Detail(%s): %v", id, err)
		}
		if det.Coverage.State != "complete" || det.Totals == nil || det.Totals.Calls != 0 ||
			det.Totals.LastUsedAtMS == nil || *det.Totals.LastUsedAtMS != wantLastUsed[i] {
			t.Fatalf("Detail[%s] = %+v totals=%+v, want complete calls=0 lastUsed=%d", id, det.Coverage, det.Totals, wantLastUsed[i])
		}
	}

	// Explicit custom bounded window [now-1h, now) must stay strictly window-scoped (LastUsedAtMS == nil).
	explicitDet, err := svc.Detail(ctx, rotatedIdle25hKeyID, testNowMS-3_600_000, testNowMS)
	if err != nil {
		t.Fatalf("Detail(explicit 1h): %v", err)
	}
	if explicitDet.Totals == nil || explicitDet.Totals.LastUsedAtMS != nil {
		t.Fatalf("explicit 1h window LastUsedAtMS = %+v, want nil", explicitDet.Totals)
	}

	// When raw events in (call1NewMS, windowFromMS) were purged, prior LastUsedAtMS must be suppressed as nil.
	purgedTS := call1NewMS + 60_000
	if _, err := db.ExecContext(ctx, `insert into usage_archive_deleted_coverage_daily (
		utc_day, deleted_event_count, min_timestamp_ms, max_timestamp_ms
	) values (?, 1, ?, ?)`, purgedTS/86_400_000, purgedTS, purgedTS); err != nil {
		t.Fatalf("insert archive deletion: %v", err)
	}
	purgedSum, err := svc.Summary(ctx, []identity.APIKeyID{rotatedIdle25hKeyID})
	if err != nil {
		t.Fatalf("Summary(after raw delete in gap): %v", err)
	}
	if purgedSum.Items[0].LastUsedAtMS != nil {
		t.Fatalf("Summary LastUsedAtMS after gap deletion = %v, want nil", *purgedSum.Items[0].LastUsedAtMS)
	}
	purgedDet, err := svc.Detail(ctx, rotatedIdle25hKeyID, 0, 0)
	if err != nil {
		t.Fatalf("Detail(after raw delete in gap): %v", err)
	}
	if purgedDet.Totals == nil || purgedDet.Totals.LastUsedAtMS != nil {
		t.Fatalf("Detail LastUsedAtMS after gap deletion = %+v, want nil", purgedDet.Totals)
	}
}

func TestNativeKeyWithDeferredBindingAfterCreationReportsPartialCoverage(t *testing.T) {
	repo := &fakeRepo{
		collectingAt: testNowMS - 10_000_000,
		projection:   ports.ProjectionState{Status: "ready", LastProcessedEventID: 4, TargetEventID: 4, LatestEventID: 4},
		bindings: map[identity.APIKeyID]ports.Binding{
			testKeyID: {
				APIKeyID:            testKeyID,
				FirstSeenAtMS:       testNowMS - 3_600_000,
				Origin:              "workspace",
				IdentityCreatedAtMS: testNowMS - 7_200_000,
			},
		},
		windows: map[identity.APIKeyID]ports.Window{},
	}
	svc := newTestService(repo, PhaseRunning)
	ctx := context.Background()

	det, err := svc.Detail(ctx, testKeyID, 0, 0)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if det.Coverage.State != string(ports.StatePartial) || det.Coverage.Reason != "window_partial" {
		t.Fatalf("Detail coverage with IdentityCreatedAtMS < FirstSeenAtMS = %+v, want partial/window_partial", det.Coverage)
	}
	sum, err := svc.Summary(ctx, []identity.APIKeyID{testKeyID})
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if sum.Coverage.State != string(ports.StatePartial) || sum.Items[0].Coverage.State != string(ports.StatePartial) {
		t.Fatalf("Summary coverage with IdentityCreatedAtMS < FirstSeenAtMS = %+v, want partial", sum)
	}
}

func TestPreCollectionPriorLastUsedSuppressedAcrossUncollectedGap(t *testing.T) {
	priorPreCollectionTS := testNowMS - 40*3_600_000
	collectingAt := testNowMS - 30*3_600_000
	repo := &fakeRepo{
		collectingAt: collectingAt,
		projection:   ports.ProjectionState{Status: "ready", LastProcessedEventID: 4, TargetEventID: 4, LatestEventID: 4},
		bindings: map[identity.APIKeyID]ports.Binding{
			testKeyID: {
				APIKeyID:            testKeyID,
				FirstSeenAtMS:       testNowMS - 48*3_600_000,
				Origin:              "adopted",
				IdentityCreatedAtMS: testNowMS - 48*3_600_000,
			},
		},
		windows: map[identity.APIKeyID]ports.Window{
			testKeyID: {
				Totals: ports.Totals{
					Calls:        0,
					LastUsedAtMS: &priorPreCollectionTS,
				},
			},
		},
	}
	svc := newTestService(repo, PhaseRunning)
	ctx := context.Background()

	det, err := svc.Detail(ctx, testKeyID, 0, 0)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if det.Coverage.State != string(ports.StateComplete) || det.Totals == nil || det.Totals.LastUsedAtMS != nil {
		t.Fatalf("Detail with pre-collectingAt prior LastUsedAtMS = %+v totals=%+v, want complete + nil LastUsedAtMS", det.Coverage, det.Totals)
	}
	sum, err := svc.Summary(ctx, []identity.APIKeyID{testKeyID})
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if sum.Items[0].Coverage.State != string(ports.StateComplete) || sum.Items[0].LastUsedAtMS != nil {
		t.Fatalf("Summary with pre-collectingAt prior LastUsedAtMS = %+v, want complete + nil LastUsedAtMS", sum.Items[0])
	}
}
