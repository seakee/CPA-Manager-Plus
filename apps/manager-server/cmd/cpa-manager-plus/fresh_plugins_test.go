package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWithSupportPluginHeaderAnnouncesCapabilityOnSuccess(t *testing.T) {
	cases := []struct {
		name     string
		target   string
		status   int
		wantHead string
	}{
		// Full Web reads this header off any successful response to learn whether
		// the saved connection can serve plugins. This test holds the proven
		// capability constant so it isolates the status/wiring rule; the
		// capability itself is proven separately below.
		{"management list", "/v0/management/plugins", http.StatusOK, "1"},
		{"bridge resource", "/console/api/v2/cpamp/config", http.StatusOK, "1"},
		{"created", "/v0/management/plugin-store/official/install", http.StatusCreated, "1"},
		{"no content", "/v0/management/plugins/demo/enabled", http.StatusNoContent, "1"},
		// A failure must not claim the deployment can serve plugins.
		{"unauthorized", "/v0/management/plugins", http.StatusUnauthorized, ""},
		{"conflict", "/v0/management/plugins/demo/enabled", http.StatusConflict, ""},
		{"server error", "/v0/management/plugins", http.StatusServiceUnavailable, ""},
		// Plugin resources keep their own sandbox contract and must not advertise
		// management capability to plugin code.
		{"plugin resource", "/v0/resource/plugins/demo/index.html", http.StatusOK, ""},
		// Private internal namespaces are not a public contract.
		{"internal namespace", "/internal/routing/fresh-authority", http.StatusNoContent, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			handler := withSupportPluginHeader(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(testCase.status)
			}), func(*http.Request) bool { return true })
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, testCase.target, nil))
			if got := recorder.Header().Get(supportPluginHeader); got != testCase.wantHead {
				t.Fatalf("header = %q, want %q", got, testCase.wantHead)
			}
		})
	}
}

// A handler that writes a body without an explicit status commits 200, and Full
// Web must still see the capability on it.
func TestWithSupportPluginHeaderImpliedStatus(t *testing.T) {
	handler := withSupportPluginHeader(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}), func(*http.Request) bool { return true })
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v0/management/plugins", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get(supportPluginHeader); got != "1" {
		t.Fatalf("header = %q, want 1", got)
	}
	if recorder.Body.String() != `{"ok":true}` {
		t.Fatalf("body = %q", recorder.Body.String())
	}
}

func TestPluginResourceRequestPredicate(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   bool
	}{
		{"canonical", "/v0/resource/plugins/demo/index.html", true},
		{"with query", "/v0/resource/plugins/demo/index.html?v=7", true},
		{"nested", "/v0/resource/plugins/demo/assets/app.js", true},
		{"encoded separator", "/v0/resource/plugins%2fdemo/index.html", false},
		{"dot segment", "/v0/resource/plugins/../console", false},
		{"doubled separator", "/v0/resource/plugins//demo/index.html", false},
		{"management path", "/v0/management/plugins", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, testCase.target, nil)
			if got := pluginResourceRequest(request); got != testCase.want {
				t.Fatalf("predicate = %v, want %v", got, testCase.want)
			}
		})
	}
}

// A connection that cannot serve plugins must not be advertised as able to.
// The header is the navigation's only input, so a route existing is not enough:
// an External or Private connection without a proven v8 plugin endpoint would
// otherwise show plugin management for a CPA that has none.
func TestSupportPluginHeaderRequiresProvenCapability(t *testing.T) {
	handler := func(capable bool) http.Handler {
		return withSupportPluginHeader(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}), func(*http.Request) bool { return capable })
	}

	proven := httptest.NewRecorder()
	handler(true).ServeHTTP(proven, httptest.NewRequest(http.MethodGet, "/v0/management/plugins", nil))
	if got := proven.Header().Get(supportPluginHeader); got != "1" {
		t.Fatalf("proven connection header = %q, want 1", got)
	}

	unproven := httptest.NewRecorder()
	handler(false).ServeHTTP(unproven, httptest.NewRequest(http.MethodGet, "/v0/management/plugins", nil))
	if got := unproven.Header().Get(supportPluginHeader); got != "" {
		t.Fatalf("unproven connection advertised plugin support: %q", got)
	}
	// The request still succeeded: capability is not authorization.
	if unproven.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", unproven.Code)
	}
}

// A read never waits for the probe. The last answer is served immediately, the
// refresh happens on the probe's own goroutine, and at most one refresh is in
// flight so a burst after expiry is one CPA round trip.
func TestPluginCapabilityProbeNeverBlocksTheReader(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	var calls atomic.Int32
	probe := newPluginCapabilityProbe(time.Hour, time.Minute, func(context.Context) bool {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return true
	})
	// Run the refresh inline so the test controls when it completes, instead of
	// racing a goroutine.
	probe.launch = func(refresh func()) { go refresh() }

	readBack := make(chan bool, 1)
	go func() { readBack <- probe.capable(context.Background()) }()
	select {
	case answer := <-readBack:
		if answer {
			t.Fatal("an unproven capability was reported")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("capable blocked on the probe")
	}
	<-started // the single refresh is now in flight and held open

	// A second read while the first refresh is stuck must also return at once,
	// and must not start another proof.
	for range 3 {
		readBack := make(chan bool, 1)
		go func() { readBack <- probe.capable(context.Background()) }()
		select {
		case <-readBack:
		case <-time.After(2 * time.Second):
			t.Fatal("a read blocked while a refresh was in flight")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("probe calls = %d, want exactly 1 concurrent refresh", got)
	}
	close(release)
	// Drain the completion so the cached answer is visible to the assertions.
	for range 100 {
		if probe.capable(context.Background()) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the completed proof was never served")
}

// The refresh uses its own detached context with its own deadline, so a
// cancelled caller neither cancels the proof nor caches its failure.
func TestPluginCapabilityProbeIgnoresCallerCancellation(t *testing.T) {
	var sawDeadline atomic.Bool
	probe := newPluginCapabilityProbe(time.Hour, time.Minute, func(ctx context.Context) bool {
		// The probe must be able to work even though the request that triggered it
		// was cancelled.
		if _, ok := ctx.Deadline(); ok {
			sawDeadline.Store(true)
		}
		select {
		case <-ctx.Done():
			return false
		default:
		}
		time.Sleep(20 * time.Millisecond)
		return !errors.Is(ctx.Err(), context.Canceled)
	})
	probe.launch = func(refresh func()) { go refresh() }

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if probe.capable(cancelled) {
		t.Fatal("an unproven capability was reported")
	}
	for range 200 {
		if probe.capable(context.Background()) {
			if !sawDeadline.Load() {
				t.Fatal("the probe ran without its own deadline")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a cancelled caller poisoned the cached answer")
}

// A probe with nothing to ask proves nothing: the default is not capable, so a
// wiring mistake narrows the navigation instead of widening it.
func TestPluginCapabilityProbeWithoutEvidenceIsNotCapable(t *testing.T) {
	var unset *pluginCapabilityProbe
	if unset.capable(context.Background()) {
		t.Fatal("a nil probe claimed capability")
	}
	if newPluginCapabilityProbe(time.Hour, time.Minute, nil).capable(context.Background()) {
		t.Fatal("a probe without evidence claimed capability")
	}
}

// forget drops the stored answer so a changed connection is not judged by the
// previous one.
func TestPluginCapabilityProbeForgets(t *testing.T) {
	var answer atomic.Bool
	probe := newPluginCapabilityProbe(time.Hour, time.Minute, func(context.Context) bool {
		return answer.Load()
	})
	// Run the refresh inline: a read then returns what was known before it, and
	// the answer it produced is what the next read sees.
	probe.launch = func(refresh func()) { refresh() }
	if probe.capable(context.Background()) {
		t.Fatal("first answer should be false")
	}
	answer.Store(true)
	// Cached: still the old answer, and no new proof.
	if probe.capable(context.Background()) {
		t.Fatal("the cached answer changed without forget")
	}
	probe.forget()
	// The first read after forget still reports the value known before it...
	if probe.capable(context.Background()) {
		t.Fatal("a read reported a proof it had not yet produced")
	}
	// ...and the next read observes the proof forget requested.
	if !probe.capable(context.Background()) {
		t.Fatal("forget did not re-prove")
	}
}

// Liveness endpoints answer for the process, so they must not carry the plugin
// capability header and must not be the request that triggers a proof.
func TestSupportPluginHeaderExcludesLiveness(t *testing.T) {
	for _, path := range []string{"/health", "/healthz", "/ready", "/readyz", "/internal/anything"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if supportPluginHeaderApplies(request) {
			t.Fatalf("%s carried the plugin capability header", path)
		}
	}
	for _, path := range []string{"/console/api/v2/operation", "/login"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if !supportPluginHeaderApplies(request) {
			t.Fatalf("%s should carry the plugin capability header", path)
		}
	}
}

// A hung CPA must not stall a routed request: /health is answered without ever
// consulting the probe, and a UI path returns on the last known answer.
func TestHungCPADoesNotStallRequests(t *testing.T) {
	hung := newPluginCapabilityProbe(time.Hour, time.Minute, func(ctx context.Context) bool {
		<-ctx.Done()
		return false
	})
	hung.launch = func(refresh func()) { go refresh() }

	handler := withSupportPluginHeader(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), func(*http.Request) bool { return hung.capable(context.Background()) })

	for _, path := range []string{"/health", "/console/api/v2/operation"} {
		done := make(chan struct{})
		go func() {
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s stalled on a hung CPA", path)
		}
	}
}

// A proof that started before a connection change must not land afterwards.
// Without the epoch, the stale result is stamped fresh and served as the new
// connection's answer for a full TTL.
func TestPluginCapabilityProbeDiscardsAProofThatOutlivedItsConnection(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var calls atomic.Int32
	// The first proof answers for the old connection, which could serve plugins;
	// every later proof answers for the new one, which cannot. That difference is
	// what makes a stale landing observable.
	probe := newPluginCapabilityProbe(time.Hour, time.Minute, func(context.Context) bool {
		first := calls.Add(1) == 1
		if first {
			started <- struct{}{}
			<-release
		}
		return first
	})
	probe.launch = func(refresh func()) { go refresh() }

	// Start the proof against the old connection and hold it open.
	probe.capable(context.Background())
	<-started
	// The connection is saved now: everything in flight describes the old one.
	probe.forget()
	close(release)

	// The stale proof must never become an answer. The new connection's own proof
	// runs on the next read and answers false.
	for range 200 {
		if probe.capable(context.Background()) {
			t.Fatal("a proof of the previous connection was served as the new one's answer")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := calls.Load(); got < 2 {
		t.Fatalf("probe calls = %d, want a fresh proof after forget", got)
	}
}

// An old-epoch proof still in flight when completion calls reprove must neither
// block the new epoch's proof from starting nor overwrite the completed answer
// when the stale probe eventually returns.
func TestPluginCapabilityProbeReproveSupersedesInFlightEpoch(t *testing.T) {
	oldEntered := make(chan struct{})
	releaseOld := make(chan struct{})
	oldDone := make(chan struct{})
	var calls, launches atomic.Int32
	probe := newPluginCapabilityProbe(time.Hour, time.Minute, func(context.Context) bool {
		if calls.Add(1) == 1 {
			close(oldEntered)
			<-releaseOld
			return false
		}
		return true
	})
	probe.launch = func(refresh func()) {
		if launches.Add(1) == 1 {
			go func() {
				defer close(oldDone)
				refresh()
			}()
			return
		}
		go refresh()
	}
	if probe.capable(context.Background()) {
		t.Fatal("probe reported capable before first proof finished")
	}
	<-oldEntered

	probe.reprove(context.Background())
	if !probe.capable(context.Background()) {
		t.Fatal("reprove did not establish capability while old epoch was in flight")
	}
	close(releaseOld)
	<-oldDone
	if !probe.capable(context.Background()) {
		t.Fatal("late old-epoch completion clobbered reproven capability")
	}
}

// Concurrent reads, forgets and refreshes must be safe under -race: the epoch
// and the in-flight flag are the only shared state the answer depends on.
func TestPluginCapabilityProbeIsRaceFree(t *testing.T) {
	var answer atomic.Bool
	probe := newPluginCapabilityProbe(time.Millisecond, time.Minute, func(context.Context) bool {
		time.Sleep(time.Millisecond)
		return answer.Load()
	})
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				if worker%4 == 0 {
					probe.forget()
				}
				if worker%5 == 0 {
					answer.Store(worker%2 == 0)
				}
				_ = probe.capable(context.Background())
			}
		}()
	}
	wg.Wait()
	// Quiesce the last refresh before the test's t.TempDir-style cleanup.
	probe.forget()
	for range 100 {
		if probe.capable(context.Background()) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}
