package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	codexinspectionsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/codexinspection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshrouting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	usagesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/usage"
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

func TestOpenedCloseDrainsCollectorAuthRejectionWithoutDeadlockAndBeforeRejectionClose(t *testing.T) {
	fake := newExternalUsageCPA(t, externalAuthorityManagement)
	t.Cleanup(fake.server.Close)
	fake.enabled.Store(true)
	normalized, err := cpa.NormalizeV8BaseURL(fake.server.URL)
	if err != nil {
		t.Fatal(err)
	}

	dbDir, err := os.MkdirTemp(t.TempDir(), "fresh-collector-close-*")
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

	app := newFreshApplication(opened, config.Config{
		CollectorMode: "http",
		PollInterval:  10 * time.Millisecond,
	}, nil)
	app.runCtx = t.Context()
	quietProjection(app)
	protected, err := opened.Protector.ProtectString(externalAuthorityManagement)
	if err != nil {
		t.Fatal(err)
	}
	op := opened.Record.Operation
	updated, err := app.store.Update(t.Context(), op.Identity, op.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
			OperationID: op.Identity.OperationID, Revision: 1, Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
		}
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.ConnectExternal, Stage: "committed",
			TargetIdentity: freshkeyapply.ExternalIdentity(normalized), TargetGeneration: 1,
		}
		r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: normalized, ManagementKey: protected}
		r.Operation.ProductComplete = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	app.externalSource = externalInstallSource(app.store, opened.Protector)

	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	callbackFinished := make(chan struct{})
	var enterOnce, finishOnce sync.Once
	app.beforeCollectorAuthRejectionLock = func(uint64) {
		enterOnce.Do(func() { close(callbackEntered) })
		<-releaseCallback
	}
	app.afterCollectorAuthRejection = func(uint64) {
		finishOnce.Do(func() { close(callbackFinished) })
	}

	app.startUsageCollection(updated.Operation)
	fake.rejects.Store(true)
	select {
	case <-callbackEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("Collector did not enter auth-rejection callback after upstream 401")
	}

	closeDone := make(chan error, 1)
	closed = true
	go func() {
		closeDone <- opened.Close()
	}()
	// Release the rejection callback once opened.Close() is running; Close() must
	// not hold usageStartMu across StopAndWait and must keep rejection open until
	// the callback finishes.
	close(releaseCallback)
	select {
	case closeErr := <-closeDone:
		if closeErr != nil {
			t.Fatalf("opened.Close(): %v", closeErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opened.Close() deadlocked against Collector auth-rejection callback")
	}
	select {
	case <-callbackFinished:
	default:
		t.Fatal("opened.Close() returned before Collector auth-rejection callback finished")
	}
	if err := os.RemoveAll(dbDir); err != nil {
		t.Fatalf("os.RemoveAll(dbDir) after Collector rejection close: %v", err)
	}
}

func TestOpenedCloseDrainsUpdateChecksAndLegacyImportSessions(t *testing.T) {
	dbDir, err := os.MkdirTemp(t.TempDir(), "fresh-updates-import-close-*")
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

	codexEntered := make(chan struct{})
	codexCanceled := make(chan struct{})
	var codexEnterOnce, codexCancelOnce sync.Once
	blockingCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" {
			codexEnterOnce.Do(func() { close(codexEntered) })
			<-r.Context().Done()
			codexCancelOnce.Do(func() { close(codexCanceled) })
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(blockingCPA.Close)

	app := newFreshApplication(opened, config.Config{
		DataDir:                   dbDir,
		DBPath:                    paths.Database,
		CPAUpstreamURL:            blockingCPA.URL,
		ManagementKey:             "test-codex-mgmt-key",
		UsageImportChunkBytes:     64 * 1024,
		UsageImportDiskQuotaBytes: 1024 * 1024,
		UsageImportMaxSessions:    2,
		UsageImportSessionTTL:     time.Hour,
	}, nil)
	op := opened.Record.Operation
	if _, err := app.store.Update(t.Context(), op.Identity, op.Revision, func(r *freshstore.Record) error {
		r.Operation.GatewayPending = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	updateEntered := make(chan struct{})
	updateExited := make(chan struct{})
	app.launchBackground(func() {
		app.runFreshUpdateChecksWith(app.lifecycleContext(t.Context()), func(workerCtx context.Context) {
			close(updateEntered)
			<-workerCtx.Done()
			close(updateExited)
		})
	})
	<-updateEntered
	if _, err := app.legacyApp.CodexInspectionService.Start(t.Context(), codexinspectionsvc.RunRequest{}); err != nil {
		t.Fatalf("CodexInspectionService.Start: %v", err)
	}
	select {
	case <-codexEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("CodexInspectionService did not enter upstream auth-files fetch")
	}

	var lines strings.Builder
	for i := range 200 {
		lines.WriteString(`{"event_hash":"h-`)
		lines.WriteString(strings.Repeat("a", 8))
		lines.WriteByte(byte('0' + (i % 10)))
		lines.WriteByte(byte('0' + ((i / 10) % 10)))
		lines.WriteByte(byte('0' + ((i / 100) % 10)))
		lines.WriteString(`","timestamp_ms":1,"timestamp":"2026-01-01T00:00:00Z","model":"gpt-test"}` + "\n")
	}
	payload := lines.String()
	sess, err := app.legacyApp.UsageService.CreateImportSession(t.Context(), "events.jsonl", int64(len(payload)), "")
	if err != nil {
		t.Fatalf("CreateImportSession: %v", err)
	}
	if _, err := app.legacyApp.UsageService.WriteImportSessionChunk(t.Context(), sess.ID, 0, int64(len(payload)), strings.NewReader(payload)); err != nil {
		t.Fatalf("WriteImportSessionChunk: %v", err)
	}
	if _, err := app.legacyApp.UsageService.CompleteImportSession(t.Context(), sess.ID); err != nil {
		t.Fatalf("CompleteImportSession: %v", err)
	}

	closed = true
	if err := opened.Close(); err != nil {
		t.Fatalf("opened.Close(): %v", err)
	}
	select {
	case <-updateExited:
	default:
		t.Fatal("opened.Close() returned before runFreshUpdateChecks inner worker exited")
	}
	select {
	case <-codexCanceled:
	default:
		t.Fatal("opened.Close() returned before CodexInspectionService canceled in-flight run")
	}
	if _, codexLateErr := app.legacyApp.CodexInspectionService.Start(context.Background(), codexinspectionsvc.RunRequest{}); !errors.Is(codexLateErr, codexinspectionsvc.ErrServiceStopping) {
		t.Fatalf("CodexInspectionService.Start after Close() = %v, want ErrServiceStopping", codexLateErr)
	}
	_, lateErr := app.legacyApp.UsageService.CreateImportSession(context.Background(), "late.jsonl", 16, "")
	var importErr *usagesvc.ImportSessionError
	if !errors.As(lateErr, &importErr) || importErr.Code != usagesvc.ImportSessionErrorUnavailable {
		t.Fatalf("CreateImportSession after Close() = %v, want ImportSessionErrorUnavailable", lateErr)
	}
	if err := os.RemoveAll(dbDir); err != nil {
		t.Fatalf("os.RemoveAll(dbDir): %v", err)
	}
}

func TestFreshShutdownPreservesDBAcrossLongRuntimeMutationAndEarlyServerExit(t *testing.T) {
	t.Run("runtime_mutation_exceeding_short_http_shutdown_completes_before_db_close", func(t *testing.T) {
		const artifactID = "sha256:e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"
		startEntered := make(chan struct{})
		releaseStart := make(chan struct{})
		var startOnce sync.Once
		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/runtime/status":
				_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"offline","cpaObservedVersion":"v8.0.55","capabilities":["start","stop"],"activeGatewayArtifact":{"engine":"cpa","artifactId":"` + artifactID + `","version":"v8.0.55"},"recovery":{"state":"inactive","attemptsRemaining":0}}`))
			case "/v1/runtime/operations/start":
				var reqBody struct {
					OperationID string `json:"operationId"`
				}
				_ = json.NewDecoder(r.Body).Decode(&reqBody)
				startOnce.Do(func() { close(startEntered) })
				<-releaseStart
				_, _ = w.Write([]byte(`{"operationId":"` + reqBody.OperationID + `","operationType":"start","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"succeeded"}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(supervisor.Close)

		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("shutdown-runtime-token"), 0o600); err != nil {
			t.Fatal(err)
		}
		dbDir, err := os.MkdirTemp(t.TempDir(), "fresh-runtime-shutdown-*")
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

		app := newFreshApplication(opened, config.Config{
			RuntimeURL:       supervisor.URL,
			RuntimeTokenFile: tokenPath,
			CPAUpstreamURL:   "http://127.0.0.1:8317",
		}, nil)
		protected, err := opened.Protector.ProtectString(strings.Repeat("f", 64))
		if err != nil {
			t.Fatal(err)
		}
		op := opened.Record.Operation
		if _, err := app.store.Update(t.Context(), op.Identity, op.Revision, func(r *freshstore.Record) error {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
				OperationID: op.Identity.OperationID, Revision: 1, Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
			}
			r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
				Strategy:                      bootstrap.NewManaged,
				Stage:                         "committed",
				TargetIdentity:                "runtime-01",
				TargetGeneration:              1,
				TargetArtifactID:              artifactID,
				DestinationConnectionIdentity: freshstrategy.ManagedDestinationIdentity("http://127.0.0.1:8317"),
			}
			r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: "http://127.0.0.1:8317", ManagementKey: protected}
			r.Operation.ProductComplete = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		app.routing = freshrouting.New(app.store, activeAdoptRoutingDriver{store: app.store})
		if _, err := app.runtimeSettings.SeedEmbeddedRuntimeDesiredLifecycleIfAbsent(t.Context(), model.EmbeddedRuntimeDesiredStopped); err != nil {
			t.Fatal(err)
		}

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{Handler: app}
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(func() { _ = server.Close() })

		type httpOutcome struct {
			status int
			err    error
		}
		respCh := make(chan httpOutcome, 1)
		go func() {
			req, reqErr := http.NewRequest(
				http.MethodPost,
				"http://"+listener.Addr().String()+"/control/api/v2/runtime/operations",
				strings.NewReader(`{"action":"start","requestId":"77777777-7777-4777-8777-777777777777"}`),
			)
			if reqErr != nil {
				respCh <- httpOutcome{err: reqErr}
				return
			}
			req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
			req.Header.Set("Content-Type", "application/json")
			resp, doErr := http.DefaultClient.Do(req)
			if doErr != nil {
				respCh <- httpOutcome{err: doErr}
				return
			}
			_ = resp.Body.Close()
			respCh <- httpOutcome{status: resp.StatusCode}
		}()
		<-startEntered

		// Force server.Shutdown to time out while the runtime mutation is still in
		// flight, then start opened.Close() immediately. opened.Close() must NOT
		// close SQLite or release .manager.lock until the mutation finishes.
		shutdownHTTPServer(server, 5*time.Millisecond)
		closeDone := make(chan error, 1)
		closed = true
		go func() {
			closeDone <- opened.Close()
		}()
		select {
		case prematureErr := <-closeDone:
			t.Fatalf("opened.Close() returned %v while runtime mutation was still writing journal/desired state", prematureErr)
		default:
		}

		close(releaseStart)
		out := <-respCh
		if out.err != nil || out.status != http.StatusOK {
			t.Fatalf("in-flight runtime operation across shutdown = (%d, %v), want (200, nil)", out.status, out.err)
		}
		if closeErr := <-closeDone; closeErr != nil {
			t.Fatalf("opened.Close(): %v", closeErr)
		}
		if err := os.RemoveAll(dbDir); err != nil {
			t.Fatalf("os.RemoveAll(dbDir): %v", err)
		}
	})

	t.Run("early_server_result_cancels_await_fresh_store_without_parent_cancel", func(t *testing.T) {
		parentCtx := context.Background()
		paths := legacypreflight.DefaultPaths(filepath.Join(t.TempDir(), "missing.sqlite"))
		startup := newStartupHTTPHandler(bootstrap.PublicDiscovery{})
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{Handler: startup}
		go func() { _ = server.Serve(listener) }()

		stopAwait := startAwaitFreshStore(parentCtx, config.Config{}, paths, startup)
		serverResult := make(chan error, 1)
		serverResult <- http.ErrServerClosed

		done := make(chan struct{})
		go func() {
			waitForReadOnlyStartup(parentCtx, server, serverResult)
			stopAwait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("waitForReadOnlyStartup + stopAwait hung when serverResult fired without parentCtx cancellation")
		}
		_ = runtimeservice.DefaultReconcileInterval
	})
}

func TestOpenedClosePreventsLateDatabaseMaintenanceStartFromLeakingDB(t *testing.T) {
	t.Run("start_usage_collection_gated_between_check_and_register", func(t *testing.T) {
		dbDir, err := os.MkdirTemp(t.TempDir(), "fresh-maint-race-start-*")
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
			stopFreshDatabaseMaintenance(paths.Database)
			if !closed {
				_ = opened.Close()
			}
		})

		app := newFreshApplication(opened, config.Config{DBPath: paths.Database}, nil)
		app.runCtx = t.Context()
		quietProjection(app)

		gateEntered := make(chan struct{})
		releaseGate := make(chan struct{})
		closeMarked := make(chan struct{})
		var enterOnce, markOnce sync.Once
		app.beforeDatabaseMaintenanceStart = func() {
			enterOnce.Do(func() { close(gateEntered) })
			<-releaseGate
		}
		app.afterCloseMarked = func() {
			markOnce.Do(func() { close(closeMarked) })
		}

		callerDone := make(chan struct{})
		go func() {
			defer close(callerDone)
			app.startUsageCollection(opened.Record.Operation)
		}()

		<-gateEntered
		closeDone := make(chan error, 1)
		closed = true
		go func() {
			closeDone <- opened.Close()
		}()
		<-closeMarked
		close(releaseGate)
		<-callerDone
		if err := <-closeDone; err != nil {
			t.Fatalf("opened.Close(): %v", err)
		}
		if leaked := freshDatabaseMaintenanceFor(paths.Database); leaked != nil {
			t.Fatalf("expected nil WAL maintainer after opened.Close(), got %p", leaked)
		}
		if err := os.RemoveAll(dbDir); err != nil {
			t.Fatalf("os.RemoveAll(dbDir): %v", err)
		}
	})

	t.Run("direct_maintenance_start_gated_past_full_close", func(t *testing.T) {
		dbDir, err := os.MkdirTemp(t.TempDir(), "fresh-maint-race-direct-*")
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
			stopFreshDatabaseMaintenance(paths.Database)
			if !closed {
				_ = opened.Close()
			}
		})

		app := newFreshApplication(opened, config.Config{DBPath: paths.Database}, nil)
		app.runCtx = t.Context()

		gateEntered := make(chan struct{})
		releaseGate := make(chan struct{})
		var enterOnce sync.Once
		app.beforeDatabaseMaintenanceStart = func() {
			enterOnce.Do(func() { close(gateEntered) })
			<-releaseGate
		}

		callerDone := make(chan struct{})
		go func() {
			defer close(callerDone)
			app.startDatabaseMaintenance(app.lifecycleContext(t.Context()))
		}()
		<-gateEntered

		closed = true
		if err := opened.Close(); err != nil {
			t.Fatalf("opened.Close(): %v", err)
		}
		close(releaseGate)
		<-callerDone
		if leaked := freshDatabaseMaintenanceFor(paths.Database); leaked != nil {
			t.Fatalf("expected nil WAL maintainer after post-Close resume, got %p", leaked)
		}
		if err := os.RemoveAll(dbDir); err != nil {
			t.Fatalf("os.RemoveAll(dbDir): %v", err)
		}
	})

	t.Run("reconcile_usage_monitoring_gated_before_usage_lock", func(t *testing.T) {
		dbDir, err := os.MkdirTemp(t.TempDir(), "fresh-maint-race-reconcile-*")
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
			stopFreshDatabaseMaintenance(paths.Database)
			if !closed {
				_ = opened.Close()
			}
		})

		app := newFreshApplication(opened, config.Config{DBPath: paths.Database}, nil)
		app.runCtx = t.Context()
		quietProjection(app)

		gateEntered := make(chan struct{})
		releaseGate := make(chan struct{})
		var enterOnce sync.Once
		app.beforeUsageReconcileLock = func() {
			enterOnce.Do(func() { close(gateEntered) })
			<-releaseGate
		}

		callerDone := make(chan struct{})
		go func() {
			defer close(callerDone)
			app.applyUsageMonitoringSwitch(t.Context(), opened.Record.Operation)
		}()
		<-gateEntered

		closed = true
		if err := opened.Close(); err != nil {
			t.Fatalf("opened.Close(): %v", err)
		}
		close(releaseGate)
		<-callerDone
		if leaked := freshDatabaseMaintenanceFor(paths.Database); leaked != nil {
			t.Fatalf("expected nil WAL maintainer after reconcile raced Close(), got %p", leaked)
		}
		if err := os.RemoveAll(dbDir); err != nil {
			t.Fatalf("os.RemoveAll(dbDir): %v", err)
		}
	})
}

func TestFreshShutdownUnblocksStalledRequestsWhilePreservingRuntimeMutation(t *testing.T) {
	t.Run("stalled_ordinary_post_gateway_config_and_chunk_upload_abort_while_runtime_mutation_completes", func(t *testing.T) {
		const artifactID = "sha256:e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"
		startEntered := make(chan struct{})
		releaseStart := make(chan struct{})
		var startOnce, releaseOnce sync.Once
		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/runtime/status":
				_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"offline","cpaObservedVersion":"v8.0.55","capabilities":["start","stop"],"activeGatewayArtifact":{"engine":"cpa","artifactId":"` + artifactID + `","version":"v8.0.55"},"recovery":{"state":"inactive","attemptsRemaining":0}}`))
			case "/v1/runtime/operations/start":
				var reqBody struct {
					OperationID string `json:"operationId"`
				}
				_ = json.NewDecoder(r.Body).Decode(&reqBody)
				startOnce.Do(func() { close(startEntered) })
				<-releaseStart
				_, _ = w.Write([]byte(`{"operationId":"` + reqBody.OperationID + `","operationType":"start","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"succeeded"}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(supervisor.Close)

		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("shutdown-runtime-token"), 0o600); err != nil {
			t.Fatal(err)
		}
		dbDir, err := os.MkdirTemp(t.TempDir(), "fresh-stalled-requests-shutdown-*")
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
		t.Cleanup(func() {
			releaseOnce.Do(func() { close(releaseStart) })
		})

		app := newFreshApplication(opened, config.Config{
			DataDir:                   dbDir,
			DBPath:                    paths.Database,
			RuntimeURL:                supervisor.URL,
			RuntimeTokenFile:          tokenPath,
			CPAUpstreamURL:            "http://127.0.0.1:8317",
			UsageImportChunkBytes:     64 * 1024,
			UsageImportDiskQuotaBytes: 1024 * 1024,
			UsageImportMaxSessions:    4,
			UsageImportSessionTTL:     time.Hour,
		}, nil)
		configReadBlocked := make(chan struct{})
		chunkReadBlocked := make(chan struct{})
		var configOnce, chunkOnce sync.Once
		app.onRequestBodyRead = func(r *http.Request, attempt int) {
			if attempt < 2 || r == nil || r.URL == nil {
				return
			}
			switch {
			case strings.HasSuffix(r.URL.Path, "/gateway/config/groups/routing"):
				configOnce.Do(func() { close(configReadBlocked) })
			case strings.HasSuffix(r.URL.Path, "/chunk"):
				chunkOnce.Do(func() { close(chunkReadBlocked) })
			}
		}

		protected, err := opened.Protector.ProtectString(strings.Repeat("f", 64))
		if err != nil {
			t.Fatal(err)
		}
		op := opened.Record.Operation
		if _, err := app.store.Update(t.Context(), op.Identity, op.Revision, func(r *freshstore.Record) error {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
				OperationID: op.Identity.OperationID, Revision: 1, Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
			}
			r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
				Strategy:                      bootstrap.NewManaged,
				Stage:                         "committed",
				TargetIdentity:                "runtime-01",
				TargetGeneration:              1,
				TargetArtifactID:              artifactID,
				DestinationConnectionIdentity: freshstrategy.ManagedDestinationIdentity("http://127.0.0.1:8317"),
			}
			r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: "http://127.0.0.1:8317", ManagementKey: protected}
			r.Operation.ProductComplete = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		app.routing = freshrouting.New(app.store, activeAdoptRoutingDriver{store: app.store})
		if _, err := app.runtimeSettings.SeedEmbeddedRuntimeDesiredLifecycleIfAbsent(t.Context(), model.EmbeddedRuntimeDesiredStopped); err != nil {
			t.Fatal(err)
		}

		sess, err := app.legacyApp.UsageService.CreateImportSession(t.Context(), "stalled.jsonl", 4096, "")
		if err != nil {
			t.Fatalf("CreateImportSession: %v", err)
		}

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{
			Handler:           app,
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(func() { _ = server.Close() })

		// 1a. Ordinary POST /gateway/config/groups/routing with an unfinished JSON
		// body over real TCP (never disconnects).
		stalledConfigConn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = stalledConfigConn.Close() })
		rawConfigReq := fmt.Sprintf(
			"POST /control/api/v2/gateway/config/groups/routing HTTP/1.1\r\n"+
				"Host: %s\r\n"+
				"Authorization: Bearer %s\r\n"+
				"Content-Type: application/json\r\n"+
				"Content-Length: 4096\r\n\r\n"+
				`{"requestId":"11`,
			listener.Addr().String(),
			strategyTestAdmin,
		)
		if _, err := stalledConfigConn.Write([]byte(rawConfigReq)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-configReadBlocked:
		case <-time.After(5 * time.Second):
			t.Fatal("stalled gateway/config POST did not reach blocking socket Read")
		}

		// 1b. Bridged PUT .../usage/import-sessions/:id/chunk with an unfinished
		// body over real TCP (never disconnects).
		stalledChunkConn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = stalledChunkConn.Close() })
		rawChunkReq := fmt.Sprintf(
			"PUT /control/api/v2/cpamp/v0/management/usage/import-sessions/%s/chunk?offset=0 HTTP/1.1\r\n"+
				"Host: %s\r\n"+
				"Authorization: Bearer %s\r\n"+
				"Content-Length: 4096\r\n\r\n"+
				`{"partial":true}`,
			sess.ID,
			listener.Addr().String(),
			strategyTestAdmin,
		)
		if _, err := stalledChunkConn.Write([]byte(rawChunkReq)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-chunkReadBlocked:
		case <-time.After(5 * time.Second):
			t.Fatal("stalled chunk upload did not reach blocking socket Read")
		}

		// 2. Concurrent runtime mutation blocked in Supervisor.
		type httpOutcome struct {
			status int
			err    error
		}
		mutationRespCh := make(chan httpOutcome, 1)
		go func() {
			req, reqErr := http.NewRequest(
				http.MethodPost,
				"http://"+listener.Addr().String()+"/control/api/v2/runtime/operations",
				strings.NewReader(`{"action":"start","requestId":"88888888-8888-4888-8888-888888888888"}`),
			)
			if reqErr != nil {
				mutationRespCh <- httpOutcome{err: reqErr}
				return
			}
			req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
			req.Header.Set("Content-Type", "application/json")
			resp, doErr := http.DefaultClient.Do(req)
			if doErr != nil {
				mutationRespCh <- httpOutcome{err: doErr}
				return
			}
			_ = resp.Body.Close()
			mutationRespCh <- httpOutcome{status: resp.StatusCode}
		}()
		<-startEntered

		// 3. Shutdown HTTP server with short budget (times out), then start
		// opened.Close(). Both stalled body reads must abort immediately while
		// opened.Close() continues waiting for the runtime mutation.
		shutdownHTTPServer(server, 20*time.Millisecond)
		closeDone := make(chan error, 1)
		closed = true
		go func() {
			closeDone <- opened.Close()
		}()

		for name, conn := range map[string]net.Conn{
			"gateway/config": stalledConfigConn,
			"import-chunk":   stalledChunkConn,
		} {
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			respLine, rErr := bufio.NewReader(conn).ReadString('\n')
			if rErr != nil && !errors.Is(rErr, io.EOF) {
				t.Fatalf("stalled %s request did not abort on Close(): %v", name, rErr)
			}
			if rErr == nil && strings.Contains(respLine, " 200 ") {
				t.Fatalf("stalled %s request unexpectedly succeeded: %q", name, respLine)
			}
		}

		select {
		case prematureErr := <-closeDone:
			t.Fatalf("opened.Close() returned %v before active runtime mutation finished", prematureErr)
		default:
		}

		// 4. Release the runtime mutation: it must complete with 200 OK and durable
		// desired state before DB close, and opened.Close() must finish promptly
		// while both stalled TCP sockets remain open.
		releaseOnce.Do(func() { close(releaseStart) })
		mutOut := <-mutationRespCh
		if mutOut.err != nil || mutOut.status != http.StatusOK {
			t.Fatalf("runtime mutation across stalled-body shutdown = (%d, %v), want (200, nil)", mutOut.status, mutOut.err)
		}
		select {
		case closeErr := <-closeDone:
			if closeErr != nil {
				t.Fatalf("opened.Close(): %v", closeErr)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("opened.Close() hung past budget on stalled TCP sockets")
		}
		reopened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{AdminKey: strategyTestAdmin})
		if err != nil {
			t.Fatalf("reopen store to verify durable runtime mutation: %v", err)
		}
		verifyApp := newFreshApplication(reopened, config.Config{DBPath: paths.Database}, nil)
		desired, found, err := verifyApp.runtimeSettings.LoadEmbeddedRuntimeDesiredState(t.Context())
		if err != nil || !found || desired.DesiredLifecycle != model.EmbeddedRuntimeDesiredRunning {
			_ = reopened.Close()
			t.Fatalf("desired lifecycle after shutdown = (%+v, %v, %v), want running", desired, found, err)
		}
		if err := reopened.Close(); err != nil {
			t.Fatalf("reopened.Close(): %v", err)
		}
		if err := os.RemoveAll(dbDir); err != nil {
			t.Fatalf("os.RemoveAll(dbDir): %v", err)
		}
	})

	t.Run("stalled_response_write_aborts_on_close", func(t *testing.T) {
		dbDir, err := os.MkdirTemp(t.TempDir(), "fresh-stalled-write-shutdown-*")
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

		const artifactID = "sha256:e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"
		app := newFreshApplication(opened, config.Config{DBPath: paths.Database, CPAUpstreamURL: "http://127.0.0.1:8317"}, nil)
		protected, err := opened.Protector.ProtectString(strings.Repeat("f", 64))
		if err != nil {
			t.Fatal(err)
		}
		op := opened.Record.Operation
		if _, err := app.store.Update(t.Context(), op.Identity, op.Revision, func(r *freshstore.Record) error {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
				OperationID: op.Identity.OperationID, Revision: 1, Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
			}
			r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
				Strategy:                      bootstrap.NewManaged,
				Stage:                         "committed",
				TargetIdentity:                "runtime-01",
				TargetGeneration:              1,
				TargetArtifactID:              artifactID,
				DestinationConnectionIdentity: freshstrategy.ManagedDestinationIdentity("http://127.0.0.1:8317"),
			}
			r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: "http://127.0.0.1:8317", ManagementKey: protected}
			r.Operation.ProductComplete = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		app.routing = freshrouting.New(app.store, activeAdoptRoutingDriver{store: app.store})

		writeBlocked := make(chan struct{})
		writeErrCh := make(chan error, 1)
		var blockOnce sync.Once
		payload := make([]byte, 8*1024*1024)
		app.legacy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc := http.NewResponseController(w)
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			_ = rc.Flush()
			blockOnce.Do(func() { close(writeBlocked) })
			_, wErr := w.Write(payload)
			writeErrCh <- wErr
		})

		tcpListener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{
			Handler:           app,
			ReadHeaderTimeout: 10 * time.Second,
			ConnState: func(c net.Conn, state http.ConnState) {
				if state == http.StateNew {
					if tc, ok := c.(*net.TCPConn); ok {
						_ = tc.SetWriteBuffer(1024)
					}
				}
			},
		}
		go func() { _ = server.Serve(tcpListener) }()
		t.Cleanup(func() { _ = server.Close() })

		stalledReader, err := net.DialTCP("tcp", nil, tcpListener.Addr().(*net.TCPAddr))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = stalledReader.Close() })
		_ = stalledReader.SetReadBuffer(1024)

		rawGet := fmt.Sprintf(
			"GET /control/api/v2/cpamp/v0/management/usage HTTP/1.1\r\n"+
				"Host: %s\r\n"+
				"Authorization: Bearer %s\r\n\r\n",
			tcpListener.Addr().String(),
			strategyTestAdmin,
		)
		if _, err := stalledReader.Write([]byte(rawGet)); err != nil {
			t.Fatal(err)
		}
		<-writeBlocked

		shutdownHTTPServer(server, 20*time.Millisecond)
		closeDone := make(chan error, 1)
		closed = true
		go func() {
			closeDone <- opened.Close()
		}()
		select {
		case closeErr := <-closeDone:
			if closeErr != nil {
				t.Fatalf("opened.Close(): %v", closeErr)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("opened.Close() hung on stalled HTTP response write")
		}
		if wErr := <-writeErrCh; wErr == nil {
			t.Fatal("expected stalled w.Write to fail with write deadline error on Close(), got nil")
		}
		if err := os.RemoveAll(dbDir); err != nil {
			t.Fatalf("os.RemoveAll(dbDir): %v", err)
		}
	})
}

func TestFreshShutdownInterruptsEarlyBodyCloseOnProxyLimit(t *testing.T) {
	const proxyAuthFileLimit = 10*1024*1024 + 64*1024

	runCase := func(t *testing.T, drainTimeout time.Duration, viaReadOnlyStartup bool) {
		t.Helper()
		const artifactID = "sha256:e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"
		upstreamCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(upstreamCPA.Close)

		dbDir, err := os.MkdirTemp(t.TempDir(), "fresh-proxy-limit-close-*")
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

		app := newFreshApplication(opened, config.Config{
			DataDir:        dbDir,
			DBPath:         paths.Database,
			CPAUpstreamURL: upstreamCPA.URL,
			ManagementKey:  strings.Repeat("f", 64),
		}, nil)
		app.earlyCloseDrainTimeout = drainTimeout
		nonEOFCloseEntered := make(chan struct{})
		var closeOnce sync.Once
		app.onBeforeNonEOFBodyClose = func(r *http.Request) {
			if r != nil && r.URL != nil && strings.HasSuffix(r.URL.Path, "/auth-files/status") {
				closeOnce.Do(func() { close(nonEOFCloseEntered) })
			}
		}

		protected, err := opened.Protector.ProtectString(strings.Repeat("f", 64))
		if err != nil {
			t.Fatal(err)
		}
		op := opened.Record.Operation
		if _, err := app.store.Update(t.Context(), op.Identity, op.Revision, func(r *freshstore.Record) error {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
				OperationID: op.Identity.OperationID, Revision: 1, Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
			}
			r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
				Strategy:                      bootstrap.NewManaged,
				Stage:                         "committed",
				TargetIdentity:                "runtime-01",
				TargetGeneration:              1,
				TargetArtifactID:              artifactID,
				DestinationConnectionIdentity: freshstrategy.ManagedDestinationIdentity(upstreamCPA.URL),
			}
			r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: upstreamCPA.URL, ManagementKey: protected}
			r.Operation.ProductComplete = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		app.routing = freshrouting.New(app.store, activeAdoptRoutingDriver{store: app.store})

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{
			Handler:           app,
			ReadHeaderTimeout: 10 * time.Second,
		}
		serverResult := make(chan error, 1)
		go func() { serverResult <- server.Serve(listener) }()
		t.Cleanup(func() { _ = server.Close() })

		// Declare Content-Length = limit+2, send exactly limit+1 bytes, and keep
		// the socket open without sending the final byte.
		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })

		reqHeader := fmt.Sprintf(
			"PATCH /control/api/v2/cpamp/v0/management/auth-files/status HTTP/1.1\r\n"+
				"Host: %s\r\n"+
				"Authorization: Bearer %s\r\n"+
				"Content-Type: application/json\r\n"+
				"Content-Length: %d\r\n\r\n",
			listener.Addr().String(),
			strategyTestAdmin,
			proxyAuthFileLimit+2,
		)
		if _, err := conn.Write([]byte(reqHeader)); err != nil {
			t.Fatal(err)
		}
		overLimitChunk := bytes.Repeat([]byte("a"), proxyAuthFileLimit+1)
		// Write in 64 KiB slices in a goroutine while the server reads limit+1
		// bytes so neither side blocks on loopback kernel socket buffers.
		writeErrCh := make(chan error, 1)
		go func() {
			_, wErr := io.Copy(conn, bytes.NewReader(overLimitChunk))
			writeErrCh <- wErr
		}()

		select {
		case <-nonEOFCloseEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("proxy limit request never entered non-EOF r.Body.Close()")
		}
		if wErr := <-writeErrCh; wErr != nil {
			t.Fatalf("client write limit+1 bytes: %v", wErr)
		}

		shutdownDone := make(chan error, 1)
		closed = true
		if viaReadOnlyStartup {
			startupCtx, cancelStartup := context.WithCancel(context.Background())
			cancelStartup()
			go func() {
				waitForReadOnlyStartup(startupCtx, server, serverResult)
				shutdownDone <- opened.Close()
			}()
		} else {
			shutdownHTTPServer(server, 20*time.Millisecond)
			go func() {
				shutdownDone <- opened.Close()
			}()
		}

		select {
		case closeErr := <-shutdownDone:
			if closeErr != nil {
				t.Fatalf("opened.Close(): %v", closeErr)
			}
		case <-time.After(4 * time.Second):
			t.Fatal("shutdown hung on non-EOF proxy limit r.Body.Close() without client sending final byte")
		}

		reopened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{AdminKey: strategyTestAdmin})
		if err != nil {
			t.Fatalf("fresh.Open failed to re-acquire .manager.lock after shutdown: %v", err)
		}
		if err := reopened.Close(); err != nil {
			t.Fatalf("reopened.Close(): %v", err)
		}
		if err := os.RemoveAll(dbDir); err != nil {
			t.Fatalf("os.RemoveAll(dbDir): %v", err)
		}
	}

	t.Run("closeCtx_interrupts_active_proxy_limit_body_close_immediately", func(t *testing.T) {
		runCase(t, -1, false)
	})
	t.Run("waitForReadOnlyStartup_finishes_promptly_on_stalled_proxy_limit_close", func(t *testing.T) {
		runCase(t, 0, true)
	})
}

func TestLifecycleContextUnregistersCallbacksAndReusesBoundContext(t *testing.T) {
	dbDir, err := os.MkdirTemp(t.TempDir(), "fresh-lifecycle-ctx-*")
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

	app := newFreshApplication(opened, config.Config{DBPath: paths.Database}, nil)
	bound := app.lifecycleContext(t.Context())
	if got := app.lifecycleContext(bound); got != bound {
		t.Fatal("lifecycleContext on an already-bound context did not reuse it")
	}

	// Warm up runtime and establish baseline goroutine count.
	for range 10 {
		c, cancel := context.WithCancel(t.Context())
		lc := app.lifecycleContext(c)
		cancel()
		<-lc.Done()
	}
	runtime.GC()
	runtime.Gosched()
	baseGoroutines := runtime.NumGoroutine()

	for range 2000 {
		c, cancel := context.WithCancel(t.Context())
		lc := app.lifecycleContext(c)
		cancel()
		<-lc.Done()
	}
	runtime.GC()
	runtime.Gosched()
	if after := runtime.NumGoroutine(); after > baseGoroutines+20 {
		t.Fatalf("goroutines grew from %d to %d across 2000 lifecycleContext cycles", baseGoroutines, after)
	}

	// A still-live derived context must cancel promptly when opened.Close() runs.
	liveBasis, cancelLive := context.WithCancel(context.Background())
	defer cancelLive()
	liveDerived := app.lifecycleContext(liveBasis)

	closed = true
	if err := opened.Close(); err != nil {
		t.Fatalf("opened.Close(): %v", err)
	}
	select {
	case <-liveDerived.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("live lifecycleContext did not cancel on opened.Close()")
	}
}
