package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
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

func TestPreFreshAuthoritySignalsGateBrowserRedirectAndLegacyPassthrough(t *testing.T) {
	var status atomic.Int32
	var contentType atomic.Value
	var body atomic.Value
	var uiServing atomic.Bool
	contentType.Store("")
	body.Store("")

	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case ingress.FreshRoutingAuthorityPath:
			if r.Method != http.MethodGet {
				t.Errorf("unexpected authority method: %s", r.Method)
			}
			if ct := contentType.Load().(string); ct != "" {
				w.Header().Set("Content-Type", ct)
			}
			code := int(status.Load())
			w.WriteHeader(code)
			if payload := body.Load().(string); payload != "" {
				_, _ = io.WriteString(w, payload)
			}
		case ingress.FixedBootstrapUIPath:
			if !uiServing.Load() {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "<!doctype html><div id=\"root\"></div>")
		default:
			http.NotFound(w, r)
		}
	}))
	defer manager.Close()

	var gatewayHits atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		gatewayHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer gateway.Close()

	mURL, err := url.Parse(manager.URL)
	if err != nil {
		t.Fatal(err)
	}
	gURL, err := url.Parse(gateway.URL)
	if err != nil {
		t.Fatal(err)
	}

	newHandler := func() http.Handler {
		fresh := &ingress.FreshRoutes{}
		posture := managerPreFreshPosture(managerFreshAuthority(mURL))
		h := ingress.NewFreshHandler(fresh, mURL, gURL, log.New(io.Discard, "", 0), func(ctx context.Context) bool {
			return posture(ctx) == ingress.PreFreshLegacyGateway
		})
		h.EnablePreFreshPosture(posture)
		return h
	}
	handler := newHandler()
	navigate := func(h http.Handler, target string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	validSignal := `{"bootstrapUI":true}`
	paddedAtLimit := validSignal + strings.Repeat(" ", maxPreCommitBootstrapSignalBytes-len(validSignal))

	// Generic/bare 404, false flag, unknown field, trailing data, and over-limit bodies stay closed (503, no Location).
	for _, tc := range []struct {
		name string
		ct   string
		body string
	}{
		{name: "bare-404", ct: "text/plain; charset=utf-8", body: "404 page not found\n"},
		{name: "empty-404", ct: "", body: ""},
		{name: "json-false", ct: "application/json", body: `{"bootstrapUI":false}`},
		{name: "json-unknown-field", ct: "application/json", body: `{"bootstrapUI":true,"extra":1}`},
		{name: "json-trailing-data", ct: "application/json", body: `{"bootstrapUI":true}{}`},
		{name: "padded-to-limit-plus-trailing-json", ct: "application/json", body: paddedAtLimit + `{}`},
		{name: "padded-to-limit-plus-trailing-text", ct: "application/json", body: paddedAtLimit + "trailing"},
		{name: "over-limit-valid-json-whitespace", ct: "application/json", body: paddedAtLimit + " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uiServing.Store(false)
			status.Store(http.StatusNotFound)
			contentType.Store(tc.ct)
			body.Store(tc.body)
			rec := navigate(handler, "/console")
			if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Location") != "" {
				t.Fatalf("status=%d Location=%q, want 503 without redirect", rec.Code, rec.Header().Get("Location"))
			}
		})
	}

	// Positive pre-commit 404 JSON (canonical and exact-limit valid) with Bootstrap UI serving -> 303 whose Location returns 200.
	uiServing.Store(true)
	status.Store(http.StatusNotFound)
	contentType.Store("application/json; charset=utf-8")
	for _, payload := range []string{validSignal, paddedAtLimit} {
		body.Store(payload)
		rec := navigate(handler, "/console")
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != ingress.FixedBootstrapUIPath {
			t.Fatalf("explicit bootstrap 404 (len=%d) = %d Location=%q", len(payload), rec.Code, rec.Header().Get("Location"))
		}
		if landing := navigate(handler, rec.Header().Get("Location")); landing.Code != http.StatusOK || landing.Header().Get("X-CPAMP-Route-Owner") != "manager" {
			t.Fatalf("landing (len=%d) = %d owner=%q", len(payload), landing.Code, landing.Header().Get("X-CPAMP-Route-Owner"))
		}
	}

	var rec *httptest.ResponseRecorder
	// Valid uncommitted 200 Fresh projection -> 303 whose Location returns 200.
	uncommitted := ingress.FreshRouteState{InstallationID: "install-one", Generation: "gen-one", OperationID: "op-one", Revision: 1, Target: "/console", Committed: false}
	status.Store(http.StatusOK)
	contentType.Store("application/json")
	body.Store(freshAuthorityJSON(uncommitted, ""))
	rec = navigate(handler, "/console")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != ingress.FixedBootstrapUIPath {
		t.Fatalf("uncommitted 200 = %d Location=%q", rec.Code, rec.Header().Get("Location"))
	}
	if landing := navigate(handler, rec.Header().Get("Location")); landing.Code != http.StatusOK {
		t.Fatalf("uncommitted landing = %d", landing.Code)
	}

	// Committed Fresh projection awaiting Restore, empty 200 JSON, and 503 stay closed (503, no Location).
	uiServing.Store(false)
	committed := uncommitted
	committed.Committed = true
	for _, tc := range []struct {
		name string
		code int32
		body string
	}{
		{name: "committed-200", code: http.StatusOK, body: freshAuthorityJSON(committed, "")},
		{name: "empty-200", code: http.StatusOK, body: "{}"},
		{name: "unavailable-503", code: http.StatusServiceUnavailable, body: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status.Store(tc.code)
			body.Store(tc.body)
			if got := navigate(handler, "/console"); got.Code != http.StatusServiceUnavailable || got.Header().Get("Location") != "" {
				t.Fatalf("status=%d Location=%q, want 503 without redirect", got.Code, got.Header().Get("Location"))
			}
		})
	}

	// Explicit 204 legacy Manager forwards to Gateway and retains confirmation across outage.
	status.Store(http.StatusNoContent)
	body.Store("")
	if got := navigate(handler, "/console"); got.Code != http.StatusOK || gatewayHits.Load() != 1 {
		t.Fatalf("legacy 204 = %d gatewayHits=%d", got.Code, gatewayHits.Load())
	}
	manager.Close()
	if got := navigate(handler, "/console"); got.Code != http.StatusOK || gatewayHits.Load() != 2 {
		t.Fatalf("post-outage legacy = %d gatewayHits=%d", got.Code, gatewayHits.Load())
	}

	// Fresh Ingress instance with Manager unreachable stays closed (503, no Location).
	if got := navigate(newHandler(), "/console"); got.Code != http.StatusServiceUnavailable || got.Header().Get("Location") != "" {
		t.Fatalf("unreachable Manager = %d Location=%q, want 503 without redirect", got.Code, got.Header().Get("Location"))
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
