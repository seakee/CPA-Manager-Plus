package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/ingress/internal/ingress"
)

func TestLoadConfigDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := loadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != defaultAddr || cfg.managerURL.String() != defaultManagerURL || cfg.gatewayURL.String() != defaultGatewayURL || cfg.shutdownTimeout != defaultShutdownTimeout {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadConfigShutdownTimeout(t *testing.T) {
	t.Parallel()
	cfg, err := loadConfig(func(key string) string {
		if key == "CPAMP_INGRESS_SHUTDOWN_TIMEOUT" {
			return "90s"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.shutdownTimeout != 90*time.Second {
		t.Fatalf("shutdownTimeout = %v, want 90s", cfg.shutdownTimeout)
	}
	for _, invalid := range []string{"0s", "-5s", "invalid"} {
		_, err := loadConfig(func(key string) string {
			if key == "CPAMP_INGRESS_SHUTDOWN_TIMEOUT" {
				return invalid
			}
			return ""
		})
		if err == nil {
			t.Fatalf("loadConfig(%q) succeeded", invalid)
		}
	}
}

func TestParseUpstreamRejectsAuthorityAndPathAmbiguity(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"https://manager:18317",
		"http://user:pass@manager:18317",
		"http://manager:18317/control",
		"http://manager:18317?route=control",
	} {
		if _, err := parseUpstream("TEST_URL", value, ""); err == nil {
			t.Fatalf("parseUpstream(%q) succeeded", value)
		}
	}
}

func TestLegacyGatewayRequiresExplicitManagerAuthority(t *testing.T) {
	var status atomic.Int32
	var calls atomic.Int32
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/internal/routing/fresh-authority" || r.Method != http.MethodGet {
			t.Errorf("unexpected authority request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(int(status.Load()))
		if status.Load() == http.StatusOK {
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer manager.Close()
	u, _ := url.Parse(manager.URL)
	allowed := managerLegacyGateway(managerFreshAuthority(u))
	for _, code := range []int{http.StatusNotFound, http.StatusServiceUnavailable, http.StatusOK, http.StatusForbidden, http.StatusFound} {
		status.Store(int32(code))
		if allowed(t.Context()) {
			t.Fatal("non-legacy response opened Gateway", code)
		}
	}
	status.Store(http.StatusNoContent)
	if !allowed(t.Context()) {
		t.Fatal("explicit legacy Manager was not accepted")
	}
	before := calls.Load()
	manager.Close()
	if !allowed(context.Background()) || calls.Load() != before {
		t.Fatal("legacy Manager outage broke confirmed mode")
	}
	// A new Ingress process has no legacy acknowledgement to reuse.
	if managerLegacyGateway(managerFreshAuthority(u))(t.Context()) {
		t.Fatal("restart inherited stale legacy mode")
	}
}

func TestPreFreshPostureDistinguishesBootstrapCommittedLegacyAndOutage(t *testing.T) {
	var status atomic.Int32
	var body atomic.Value
	body.Store("")
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ingress.FreshRoutingAuthorityPath || r.Method != http.MethodGet {
			t.Errorf("unexpected authority request: %s %s", r.Method, r.URL.Path)
		}
		code := int(status.Load())
		w.WriteHeader(code)
		if code == http.StatusOK {
			_, _ = io.WriteString(w, body.Load().(string))
		}
	}))
	defer manager.Close()
	u, err := url.Parse(manager.URL)
	if err != nil {
		t.Fatal(err)
	}
	posture := managerPreFreshPosture(managerFreshAuthority(u))

	// 404 (pristine Fresh, pre-prepare Fresh, or read-only startup inspection) -> PreFreshBootstrap.
	status.Store(http.StatusNotFound)
	if got := posture(t.Context()); got != ingress.PreFreshBootstrap {
		t.Fatalf("404 posture = %v, want PreFreshBootstrap", got)
	}

	// Valid uncommitted Fresh projection -> PreFreshBootstrap.
	uncommitted := ingress.FreshRouteState{InstallationID: "install-one", Generation: "gen-one", OperationID: "op-one", Revision: 1, Target: "/console", Committed: false}
	status.Store(http.StatusOK)
	body.Store(freshAuthorityJSON(uncommitted, ""))
	if got := posture(t.Context()); got != ingress.PreFreshBootstrap {
		t.Fatalf("uncommitted 200 posture = %v, want PreFreshBootstrap", got)
	}

	// Committed Fresh projection awaiting Restore after Ingress restart -> PreFreshPendingClosed.
	committed := uncommitted
	committed.Committed = true
	body.Store(freshAuthorityJSON(committed, ""))
	if got := posture(t.Context()); got != ingress.PreFreshPendingClosed {
		t.Fatalf("committed 200 posture = %v, want PreFreshPendingClosed", got)
	}

	// Empty/incomplete 200 JSON -> fail closed to PreFreshPendingClosed.
	body.Store("{}")
	if got := posture(t.Context()); got != ingress.PreFreshPendingClosed {
		t.Fatalf("empty 200 posture = %v, want PreFreshPendingClosed", got)
	}

	// 503 unavailable -> PreFreshPendingClosed.
	status.Store(http.StatusServiceUnavailable)
	if got := posture(t.Context()); got != ingress.PreFreshPendingClosed {
		t.Fatalf("503 posture = %v, want PreFreshPendingClosed", got)
	}

	// Explicit 204 legacy Manager -> PreFreshLegacyGateway and retained across outage.
	status.Store(http.StatusNoContent)
	if got := posture(t.Context()); got != ingress.PreFreshLegacyGateway {
		t.Fatalf("204 posture = %v, want PreFreshLegacyGateway", got)
	}
	manager.Close()
	if got := posture(t.Context()); got != ingress.PreFreshLegacyGateway {
		t.Fatalf("post-outage legacy posture = %v, want PreFreshLegacyGateway", got)
	}

	// Fresh Ingress process with Manager unreachable -> PreFreshPendingClosed.
	if got := managerPreFreshPosture(managerFreshAuthority(u))(t.Context()); got != ingress.PreFreshPendingClosed {
		t.Fatalf("unreachable Manager posture = %v, want PreFreshPendingClosed", got)
	}
}

func TestDrainServersWaitsForFlushingSSEStream(t *testing.T) {
	t.Parallel()
	streamStarted := make(chan struct{})
	releaseStream := make(chan struct{})

	tracker := newDrainTracker(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("ResponseWriter does not implement http.Flusher")
			return
		}
		_, _ = io.WriteString(w, "data: chunk-1\n\n")
		flusher.Flush()
		close(streamStarted)
		<-releaseStream
		_, _ = io.WriteString(w, "data: chunk-2\n\n")
		flusher.Flush()
	}))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: tracker}
	go func() { _ = server.Serve(ln) }()

	respCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/v1/chat/completions")
		if err != nil {
			errCh <- err
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			errCh <- err
			return
		}
		respCh <- string(body)
	}()

	<-streamStarted
	drainDone := make(chan error, 1)
	go func() {
		drainDone <- drainServers(context.Background(), 2*time.Second, server, nil, tracker)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", ln.Addr().String(), 50*time.Millisecond)
		if dialErr != nil {
			break
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("listener still accepted new connections during drain")
		}
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case err := <-drainDone:
		t.Fatalf("drainServers returned before SSE stream finished: %v", err)
	default:
	}

	close(releaseStream)
	if err := <-drainDone; err != nil {
		t.Fatalf("drainServers failed: %v", err)
	}
	select {
	case err := <-errCh:
		t.Fatalf("SSE client failed: %v", err)
	case body := <-respCh:
		want := "data: chunk-1\n\ndata: chunk-2\n\n"
		if body != want {
			t.Fatalf("SSE body = %q, want %q", body, want)
		}
	}
}

func TestDrainServersWaitsForHijackedStreamAndEnforcesTimeout(t *testing.T) {
	t.Parallel()
	hijackedStarted := make(chan struct{})
	releaseHijacked := make(chan struct{})

	tracker := newDrainTracker(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("ResponseWriter does not implement http.Hijacker")
			return
		}
		conn, rw, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("Hijack: %v", err)
			return
		}
		_, _ = rw.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\npart-1:")
		_ = rw.Flush()
		close(hijackedStarted)
		go func() {
			defer conn.Close()
			<-releaseHijacked
			_, _ = rw.WriteString("part-2")
			_ = rw.Flush()
		}()
	}))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: tracker}
	go func() { _ = server.Serve(ln) }()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = io.WriteString(conn, "GET /stream HTTP/1.1\r\nHost: localhost\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}

	<-hijackedStarted
	drainDone := make(chan error, 1)
	go func() {
		drainDone <- drainServers(context.Background(), 2*time.Second, server, nil, tracker)
	}()

	time.Sleep(50 * time.Millisecond)
	select {
	case err := <-drainDone:
		t.Fatalf("drainServers returned before hijacked connection closed: %v", err)
	default:
	}

	close(releaseHijacked)
	if err := <-drainDone; err != nil {
		t.Fatalf("drainServers failed: %v", err)
	}
	raw, err := io.ReadAll(bufio.NewReader(conn))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(raw), "part-1:part-2") {
		t.Fatalf("hijacked response = %q, want suffix part-1:part-2", string(raw))
	}

	// Verify timeout closes lingering hijacked connections and returns DeadlineExceeded.
	stuckStarted := make(chan struct{})
	stuckTracker := newDrainTracker(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		_, _ = rw.WriteString("HTTP/1.1 200 OK\r\n\r\n")
		_ = rw.Flush()
		close(stuckStarted)
		// Hold connection until drain timeout force-closes it.
		_, _ = io.Copy(io.Discard, conn)
		_ = conn.Close()
	}))
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server2 := &http.Server{Handler: stuckTracker}
	go func() { _ = server2.Serve(ln2) }()
	conn2, err := net.Dial("tcp", ln2.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	_, _ = io.WriteString(conn2, "GET /stuck HTTP/1.1\r\nHost: localhost\r\n\r\n")
	<-stuckStarted
	err = drainServers(context.Background(), 60*time.Millisecond, server2, nil, stuckTracker)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drainServers timeout error = %v, want DeadlineExceeded", err)
	}
}
