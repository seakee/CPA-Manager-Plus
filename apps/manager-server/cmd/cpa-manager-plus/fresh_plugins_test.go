package main

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshrouting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
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

// Streaming responses and 101 connection upgrades through `withSupportPluginHeader`
// and `app.ServeHTTP` must preserve `http.Flusher`, `http.Hijacker`, and
// `http.ResponseController`: the first streamed chunk arrives with
// `X-CPA-SUPPORT-PLUGIN: 1` before the handler finishes, and Hijack hands over
// the raw socket without touching headers after takeover.
func TestWithSupportPluginHeaderPreservesStreamingFlushAndHijack(t *testing.T) {
	t.Run("streamed chunk via ServeHTTP and externalLegacyRecorder arrives with header before handler finishes", func(t *testing.T) {
		releaseUpstream := make(chan struct{})
		closeRelease := sync.OnceFunc(func() { close(releaseUpstream) })
		upstream := newProbeCPA(t, externalAuthorityManagement)
		streamLogs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+externalAuthorityManagement {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("X-CPA-Version", "v8.0.13")
			switch r.URL.Path {
			case "/v8/management/config":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"plugins":{"dir":"/var/lib/cpa/plugins"}}`))
			case "/v0/management/logs":
				w.Header().Set("Content-Type", "text/plain")
				_, _ = w.Write([]byte("first-chunk\n"))
				w.(http.Flusher).Flush()
				<-releaseUpstream
				_, _ = w.Write([]byte("second-chunk\n"))
			default:
				upstream.server.Config.Handler.ServeHTTP(w, r)
			}
		}))
		t.Cleanup(streamLogs.Close)

		app := newExternalApp(t, streamLogs.URL, externalAuthorityManagement)
		app.routing = freshrouting.New(app.store, activeAdoptRoutingDriver{store: app.store})
		app.gatewayConfig = gatewayFor(app)
		app.pluginCapability = newPluginCapabilityProbe(pluginCapabilityTTL, pluginCapabilityProbeTimeout, app.v8PluginCapable)
		app.pluginCapability.reprove(t.Context())
		upstreamTarget, err := url.Parse(streamLogs.URL)
		if err != nil {
			t.Fatal(err)
		}
		reverseProxy := httputil.NewSingleHostReverseProxy(upstreamTarget)
		origDirector := reverseProxy.Director
		reverseProxy.Director = func(req *http.Request) {
			origDirector(req)
			req.Header.Set("Authorization", "Bearer "+externalAuthorityManagement)
		}
		app.legacy = guardFreshExternalLegacy(reverseProxy, app)
		server := httptest.NewServer(app)
		t.Cleanup(server.Close)
		t.Cleanup(closeRelease)

		reqCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, server.URL+"/control/api/v2/cpamp/v0/management/logs", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", gateAdmin)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatalf("Do streamed GET: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("streamed status = %d, want 200", resp.StatusCode)
		}
		if got := resp.Header.Get(supportPluginHeader); got != supportPluginHeaderValue {
			t.Fatalf("streamed %s = %q, want %q", supportPluginHeader, got, supportPluginHeaderValue)
		}
		reader := bufio.NewReader(resp.Body)
		firstLine, err := reader.ReadString('\n')
		if err != nil || firstLine != "first-chunk\n" {
			t.Fatalf("first streamed chunk before handler returned = %q (%v)", firstLine, err)
		}

		closeRelease()
		secondLine, err := reader.ReadString('\n')
		if err != nil || secondLine != "second-chunk\n" {
			t.Fatalf("second streamed chunk = %q (%v)", secondLine, err)
		}
	})

	t.Run("ResponseController Flush stamps header before committing underlying wire headers", func(t *testing.T) {
		release := make(chan struct{})
		closeRelease := sync.OnceFunc(func() { close(release) })
		handler := withSupportPluginHeader(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Errorf("ResponseController.Flush: %v", err)
				return
			}
			_, _ = w.Write([]byte("head-flushed\n"))
			_ = http.NewResponseController(w).Flush()
			<-release
			_, _ = w.Write([]byte("tail\n"))
		}), func(*http.Request) bool { return true })

		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		t.Cleanup(closeRelease)

		reqCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, server.URL+"/console/api/v2/operation", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if got := resp.Header.Get(supportPluginHeader); got != supportPluginHeaderValue {
			t.Fatalf("ResponseController flushed %s = %q, want %q", supportPluginHeader, got, supportPluginHeaderValue)
		}
		reader := bufio.NewReader(resp.Body)
		line, err := reader.ReadString('\n')
		if err != nil || line != "head-flushed\n" {
			t.Fatalf("first flushed line = %q (%v)", line, err)
		}
		closeRelease()
		tail, err := reader.ReadString('\n')
		if err != nil || tail != "tail\n" {
			t.Fatalf("tail line = %q (%v)", tail, err)
		}
	})

	t.Run("Hijack works via ResponseController and direct http.Hijacker without post-takeover header mutation", func(t *testing.T) {
		for _, mode := range []string{"response-controller", "direct-hijacker", "external-legacy-recorder"} {
			t.Run(mode, func(t *testing.T) {
				var capableCalls atomic.Int32
				inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					target := w
					if mode == "external-legacy-recorder" {
						var gate externalCredentialGate
						gate.acquire(r.Context())
						hold, _ := beginExternalCredentialHold(r.Context(), &gate, 0)
						target = &externalLegacyRecorder{ResponseWriter: w, hold: hold}
					}
					var (
						conn net.Conn
						rw   *bufio.ReadWriter
						err  error
					)
					if mode == "response-controller" {
						conn, rw, err = http.NewResponseController(target).Hijack()
					} else {
						conn, rw, err = target.(http.Hijacker).Hijack()
					}
					if err != nil {
						t.Errorf("Hijack (%s): %v", mode, err)
						return
					}
					defer conn.Close()
					// Late WriteHeader after Hijack must be short-circuited by the wrapper
					// without invoking capable, mutating headers, or forwarding to net/http.
					target.WriteHeader(http.StatusOK)
					if got := target.Header().Get(supportPluginHeader); got != "" {
						t.Errorf("Hijack (%s) stamped %s=%q on response headers", mode, supportPluginHeader, got)
					}
					_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: demo\r\nConnection: Upgrade\r\n\r\nupgraded\n")
					_ = rw.Flush()
				})
				handler := withSupportPluginHeader(inner, func(*http.Request) bool {
					capableCalls.Add(1)
					return true
				})
				server := httptest.NewServer(handler)
				t.Cleanup(server.Close)

				dialed, err := net.Dial("tcp", server.Listener.Addr().String())
				if err != nil {
					t.Fatal(err)
				}
				defer dialed.Close()
				_ = dialed.SetDeadline(time.Now().Add(5 * time.Second))
				if _, err := dialed.Write([]byte("GET /console/api/v2/cpamp/ws HTTP/1.1\r\nHost: example.com\r\nConnection: Upgrade\r\nUpgrade: demo\r\n\r\n")); err != nil {
					t.Fatal(err)
				}
				reader := bufio.NewReader(dialed)
				statusLine, err := reader.ReadString('\n')
				if err != nil || !strings.Contains(statusLine, "101 Switching Protocols") {
					t.Fatalf("hijacked status line = %q (%v)", statusLine, err)
				}
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						t.Fatal(err)
					}
					if line == "\r\n" {
						break
					}
					if strings.HasPrefix(strings.ToUpper(line), supportPluginHeader+":") {
						t.Fatalf("hijacked 101 response leaked %s header: %q", supportPluginHeader, line)
					}
				}
				upgradedMsg, err := reader.ReadString('\n')
				if err != nil || upgradedMsg != "upgraded\n" {
					t.Fatalf("hijacked stream = %q (%v)", upgradedMsg, err)
				}
				if got := capableCalls.Load(); got != 0 {
					t.Fatalf("Hijack (%s) called capable %d times, want 0", mode, got)
				}
			})
		}

		// Non-hijackable writers still report http.ErrNotSupported without latching committed.
		rec := httptest.NewRecorder()
		var hijackErr error
		withSupportPluginHeader(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _, hijackErr = http.NewResponseController(w).Hijack()
			w.WriteHeader(http.StatusOK)
		}), func(*http.Request) bool { return true }).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/console/api/v2/operation", nil))
		if !errors.Is(hijackErr, http.ErrNotSupported) {
			t.Fatalf("unsupported Hijack err = %v, want ErrNotSupported", hijackErr)
		}
		if got := rec.Header().Get(supportPluginHeader); got != supportPluginHeaderValue {
			t.Fatalf("fallback response after unsupported Hijack %s = %q, want %q", supportPluginHeader, got, supportPluginHeaderValue)
		}
	})
}

type spyStatusWriter struct {
	header      http.Header
	statuses    []int
	headerSnap  []string
	hijackConn  net.Conn
	hijackRW    *bufio.ReadWriter
	allowHijack bool
}

func newSpyStatusWriter() *spyStatusWriter {
	return &spyStatusWriter{header: make(http.Header)}
}

func (w *spyStatusWriter) Header() http.Header { return w.header }

func (w *spyStatusWriter) WriteHeader(status int) {
	w.statuses = append(w.statuses, status)
	w.headerSnap = append(w.headerSnap, w.header.Get(supportPluginHeader))
}

func (w *spyStatusWriter) Write(p []byte) (int, error) {
	if len(w.statuses) == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return len(p), nil
}

func (w *spyStatusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if !w.allowHijack {
		return nil, nil, http.ErrNotSupported
	}
	return w.hijackConn, w.hijackRW, nil
}

// `X-CPA-SUPPORT-PLUGIN` must be stamped only on final 2xx/3xx responses (never
// on 101 Switching Protocols or 100/103 informational statuses), and duplicate
// or post-Hijack WriteHeader calls must short-circuit without forwarding.
func TestWithSupportPluginHeaderStatusAndDuplicateWriteHeaderBoundaries(t *testing.T) {
	t.Run("WriteHeader 101 Switching Protocols neither stamps header nor allows follow-up WriteHeader", func(t *testing.T) {
		spy := newSpyStatusWriter()
		var capableCalls atomic.Int32
		withSupportPluginHeader(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusSwitchingProtocols)
			w.WriteHeader(http.StatusOK)
		}), func(*http.Request) bool {
			capableCalls.Add(1)
			return true
		}).ServeHTTP(spy, httptest.NewRequest(http.MethodGet, "/console/api/v2/cpamp/ws", nil))
		if len(spy.statuses) != 1 || spy.statuses[0] != http.StatusSwitchingProtocols {
			t.Fatalf("underlying WriteHeader statuses = %v, want [101]", spy.statuses)
		}
		if got := spy.Header().Get(supportPluginHeader); got != "" {
			t.Fatalf("101 response %s = %q, want empty", supportPluginHeader, got)
		}
		if got := capableCalls.Load(); got != 0 {
			t.Fatalf("capable calls on 101 = %d, want 0", got)
		}
	})

	t.Run("informational 100 and 103 do not stamp header early and preserve final status evaluation", func(t *testing.T) {
		for _, finalStatus := range []int{http.StatusOK, http.StatusServiceUnavailable} {
			spy := newSpyStatusWriter()
			var capableCalls atomic.Int32
			withSupportPluginHeader(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusContinue)
				w.WriteHeader(http.StatusEarlyHints)
				w.WriteHeader(finalStatus)
				w.WriteHeader(http.StatusOK)
			}), func(*http.Request) bool {
				capableCalls.Add(1)
				return true
			}).ServeHTTP(spy, httptest.NewRequest(http.MethodGet, "/console/api/v2/operation", nil))
			if len(spy.statuses) != 3 || spy.statuses[0] != http.StatusContinue || spy.statuses[1] != http.StatusEarlyHints || spy.statuses[2] != finalStatus {
				t.Fatalf("statuses (final=%d) = %v, want [100 103 %d]", finalStatus, spy.statuses, finalStatus)
			}
			if spy.headerSnap[0] != "" || spy.headerSnap[1] != "" {
				t.Fatalf("informational responses carried %s: %v", supportPluginHeader, spy.headerSnap[:2])
			}
			wantFinal := ""
			wantCalls := int32(0)
			if finalStatus == http.StatusOK {
				wantFinal = supportPluginHeaderValue
				wantCalls = 1
			}
			if spy.headerSnap[2] != wantFinal || spy.Header().Get(supportPluginHeader) != wantFinal {
				t.Fatalf("final=%d %s = %q (snap=%q), want %q", finalStatus, supportPluginHeader, spy.Header().Get(supportPluginHeader), spy.headerSnap[2], wantFinal)
			}
			if got := capableCalls.Load(); got != wantCalls {
				t.Fatalf("final=%d capable calls = %d, want %d", finalStatus, got, wantCalls)
			}
		}
	})

	t.Run("double WriteHeader short-circuits second call without mutating header or forwarding", func(t *testing.T) {
		spyFailFirst := newSpyStatusWriter()
		var failCapable atomic.Int32
		withSupportPluginHeader(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.WriteHeader(http.StatusOK)
		}), func(*http.Request) bool {
			failCapable.Add(1)
			return true
		}).ServeHTTP(spyFailFirst, httptest.NewRequest(http.MethodGet, "/console/api/v2/operation", nil))
		if len(spyFailFirst.statuses) != 1 || spyFailFirst.statuses[0] != http.StatusServiceUnavailable {
			t.Fatalf("503->200 underlying statuses = %v, want [503]", spyFailFirst.statuses)
		}
		if got := spyFailFirst.Header().Get(supportPluginHeader); got != "" || failCapable.Load() != 0 {
			t.Fatalf("503->200 %s = %q (capable=%d), want empty/0", supportPluginHeader, got, failCapable.Load())
		}

		spyOKFirst := newSpyStatusWriter()
		var okCapable atomic.Int32
		withSupportPluginHeader(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.WriteHeader(http.StatusServiceUnavailable)
		}), func(*http.Request) bool {
			okCapable.Add(1)
			return true
		}).ServeHTTP(spyOKFirst, httptest.NewRequest(http.MethodGet, "/console/api/v2/operation", nil))
		if len(spyOKFirst.statuses) != 1 || spyOKFirst.statuses[0] != http.StatusOK {
			t.Fatalf("200->503 underlying statuses = %v, want [200]", spyOKFirst.statuses)
		}
		if got := spyOKFirst.Header().Get(supportPluginHeader); got != supportPluginHeaderValue || okCapable.Load() != 1 {
			t.Fatalf("200->503 %s = %q (capable=%d), want 1/1", supportPluginHeader, got, okCapable.Load())
		}
	})

	t.Run("WriteHeader after Hijack does not forward to underlying writer or stamp header", func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer clientConn.Close()
		defer serverConn.Close()
		spy := newSpyStatusWriter()
		spy.allowHijack = true
		spy.hijackConn = serverConn
		spy.hijackRW = bufio.NewReadWriter(bufio.NewReader(serverConn), bufio.NewWriter(serverConn))
		var capableCalls atomic.Int32
		withSupportPluginHeader(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if _, _, err := http.NewResponseController(w).Hijack(); err != nil {
				t.Fatalf("Hijack: %v", err)
			}
			w.WriteHeader(http.StatusOK)
		}), func(*http.Request) bool {
			capableCalls.Add(1)
			return true
		}).ServeHTTP(spy, httptest.NewRequest(http.MethodGet, "/console/api/v2/cpamp/ws", nil))
		if len(spy.statuses) != 0 {
			t.Fatalf("WriteHeader forwarded after Hijack: %v", spy.statuses)
		}
		if got := spy.Header().Get(supportPluginHeader); got != "" || capableCalls.Load() != 0 {
			t.Fatalf("post-Hijack WriteHeader %s = %q (capable=%d), want empty/0", supportPluginHeader, got, capableCalls.Load())
		}
	})
}

func TestPluginCapabilityProbeCloseCancelsAndWaitsForInFlightRefresh(t *testing.T) {
	entered := make(chan struct{})
	exited := make(chan struct{})
	var (
		sawCancel atomic.Bool
		calls     atomic.Int32
	)
	probe := newPluginCapabilityProbe(time.Hour, time.Minute, func(ctx context.Context) bool {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		sawCancel.Store(errors.Is(ctx.Err(), context.Canceled))
		close(exited)
		return true
	})

	if probe.capable(context.Background()) {
		t.Fatal("probe reported capable before refresh completed")
	}
	<-entered

	probe.close()
	select {
	case <-exited:
	default:
		t.Fatal("probe.close() returned before in-flight refresh exited")
	}
	if !sawCancel.Load() {
		t.Fatal("probe.close() did not cancel in-flight refresh context")
	}
	if probe.capable(context.Background()) {
		t.Fatal("closed probe reported capable")
	}
	probe.reprove(context.Background())
	if got := calls.Load(); got != 1 {
		t.Fatalf("probe calls after close = %d, want 1", got)
	}
}

func TestOpenedCloseDrainsInFlightPluginCapabilityRefreshBeforeClosingDB(t *testing.T) {
	probeEntered := make(chan struct{})
	var enterOnce sync.Once
	externalCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+externalAuthorityManagement {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-CPA-Version", "v8.0.55")
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v8/management/config" {
			_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"/data/auth"},"access":{"api-keys":["ext-key-1"]}}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(externalCPA.Close)

	dbDir, err := os.MkdirTemp(t.TempDir(), "fresh-db-*")
	if err != nil {
		t.Fatal(err)
	}
	paths := legacypreflight.DefaultPaths(filepath.Join(dbDir, "manager.sqlite"))
	paths.Config = filepath.Join(dbDir, "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = opened.Close()
		}
	})

	app := newFreshApplication(opened, config.Config{}, nil)
	app.strategy = freshstrategy.New(freshstrategy.Options{
		Protector:     opened.Protector,
		Qualification: bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true},
	})
	op := opened.Record.Operation
	updated, err := app.store.Update(t.Context(), op.Identity, op.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
			OperationID: op.Identity.OperationID, Revision: 1, Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	refreshExited := make(chan struct{})
	var exitOnce sync.Once
	origProbe := app.pluginCapability.probe
	app.pluginCapability.probe = func(ctx context.Context) bool {
		defer exitOnce.Do(func() { close(refreshExited) })
		_ = origProbe(ctx)
		enterOnce.Do(func() { close(probeEntered) })
		<-ctx.Done()
		return false
	}
	app.routing = freshrouting.New(app.store, activeAdoptRoutingDriver{store: app.store})
	if res := a3Post(t, app, updated.Operation.Revision, externalCPA.URL, externalAuthorityManagement); res.Code != http.StatusOK {
		t.Fatalf("ConnectExternal commit = %d %s, want 200", res.Code, res.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/control/api/v2/operation", nil)
	req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	opRes := httptest.NewRecorder()
	app.ServeHTTP(opRes, req)
	if opRes.Code != http.StatusOK {
		t.Fatalf("GET /control/api/v2/operation = %d %s, want 200", opRes.Code, opRes.Body.String())
	}
	select {
	case <-probeEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("200 response did not launch background plugin capability refresh")
	}

	closed = true
	if err := opened.Close(); err != nil {
		t.Fatalf("opened.Close(): %v", err)
	}
	select {
	case <-refreshExited:
	default:
		t.Fatal("opened.Close() returned while plugin capability refresh was still running")
	}
	if err := os.RemoveAll(dbDir); err != nil {
		t.Fatalf("os.RemoveAll(dbDir) right after opened.Close(): %v", err)
	}
}
