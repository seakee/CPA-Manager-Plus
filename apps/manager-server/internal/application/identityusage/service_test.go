package identityusage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityusage"
)

const (
	testNowMS   = int64(1_760_000_000_000)
	testKeyID   = identity.APIKeyID("a1b2c3d4e5f60718293a4b5c6d7e8f90")
	testOtherID = identity.APIKeyID("0f1e2d3c4b5a69788796a5b4c3d2e1f0")
)

type fakeRepo struct {
	bindings      map[identity.APIKeyID]ports.Binding
	bindingErrs   map[identity.APIKeyID]error
	collectingAt  int64
	projection    ports.ProjectionState
	projectionErr error
	windows       map[identity.APIKeyID]ports.Window
	series        []ports.Bucket
	rawDeleted    bool
	incomplete    []ports.IncompleteDay
	windowFromMS  int64
	windowToMS    int64
	markedAt      int64
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

func (f *fakeRepo) Windows(_ context.Context, ids []identity.APIKeyID, fromMS, toMS int64) (map[identity.APIKeyID]ports.Window, error) {
	f.windowFromMS, f.windowToMS = fromMS, toMS
	result := map[identity.APIKeyID]ports.Window{}
	for _, id := range ids {
		result[id] = f.windows[id]
	}
	return result, nil
}

func (f *fakeRepo) Series(context.Context, identity.APIKeyID, int64, int64, int64) ([]ports.Bucket, error) {
	return f.series, nil
}

func (f *fakeRepo) RawDeletedIn(context.Context, int64, int64) (bool, error) {
	return f.rawDeleted, nil
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
		bindings:     map[identity.APIKeyID]ports.Binding{testKeyID: {APIKeyID: testKeyID, FirstSeenAtMS: 1}},
		collectingAt: 1,
		projection:   ports.ProjectionState{Status: "ready", LastProcessedEventID: 4, TargetEventID: 4, LatestEventID: 4},
		windows: map[identity.APIKeyID]ports.Window{testKeyID: {Totals: ports.Totals{
			Calls: 3, Failure: 1, InputTokens: 30, OutputTokens: 15, TotalTokens: 45, LastUsedAtMS: &lastUsedAtMS,
		}}},
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

	// A key created inside the requested window truncates it as well.
	repo = healthy()
	repo.bindings = map[identity.APIKeyID]ports.Binding{testKeyID: {APIKeyID: testKeyID, FirstSeenAtMS: testNowMS - 3_600_000}}
	response = detail(t, repo, PhaseRunning)
	if response.FromMS != testNowMS-3_600_000 {
		t.Fatalf("from = %d, want the attribution start", response.FromMS)
	}
	if response.Coverage.State != "partial" || response.Coverage.Reason != "window_partial" {
		t.Fatalf("coverage = %+v", response.Coverage)
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
	if summary.Items[0].Calls != 3 || summary.Items[0].Coverage.State != "complete" ||
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
