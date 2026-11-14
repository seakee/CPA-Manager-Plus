package ingress

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	return parsed
}

func applyControlState(t *testing.T, routes *FreshRoutes, state FreshRouteState, wantCode int) {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, FreshRoutingControlPath, bytes.NewReader(raw))
	w := httptest.NewRecorder()
	routes.ControlHandler(func(context.Context) (FreshRouteState, error) { return state, nil }).ServeHTTP(w, req)
	if w.Code != wantCode {
		t.Fatalf("PUT control status = %d, want %d: %s", w.Code, wantCode, w.Body.String())
	}
}

func TestTwoPrefixIngressStripsSupportedProtocolsAndRejectsUnprefixedAndManagementPaths(t *testing.T) {
	var managerHits atomic.Int32
	var gatewayHits atomic.Int32
	var lastGatewayPath atomic.Value
	var lastGatewayEscaped atomic.Value
	var lastGatewayQuery atomic.Value

	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		managerHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "manager:"+r.URL.Path)
	}))
	defer manager.Close()

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayHits.Add(1)
		lastGatewayPath.Store(r.URL.Path)
		lastGatewayEscaped.Store(r.URL.EscapedPath())
		lastGatewayQuery.Store(r.URL.RawQuery)

		if strings.EqualFold(r.Header.Get("Connection"), "Upgrade") && strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			hj, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "hijack unsupported", http.StatusInternalServerError)
				return
			}
			conn, rw, err := hj.Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nX-Upstream-WS-Path: " + r.URL.Path + "\r\n\r\nws-ok")
			_ = rw.Flush()
			return
		}
		if r.URL.Query().Get("stream") == "sse" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			_, _ = io.WriteString(w, "data: chunk-1\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			_, _ = io.WriteString(w, "data: chunk-2\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "gateway:"+r.URL.Path+":"+string(body))
	}))
	defer gateway.Close()

	routes := &FreshRoutes{}
	h := NewFreshHandler(routes, mustURL(t, manager.URL), mustURL(t, gateway.URL), log.New(io.Discard, "", 0), nil)
	h.EnableFreshGatewayAdmission(func(context.Context) bool { return true })
	public := httptest.NewServer(h)
	defer public.Close()

	applyControlState(t, routes, FreshRouteState{
		InstallationID:     "inst-1",
		Generation:         "gen-1",
		OperationID:        "op-1",
		Revision:           1,
		Target:             "/cpamp-admin",
		InferenceTarget:    "/cpamp-api",
		SharedEntry:        false,
		Committed:          true,
		InferenceCommitted: true,
	}, http.StatusOK)

	// 1. Supported CPA v8.0.55 inference protocol families strip /cpamp-api cleanly.
	supportedCases := []struct {
		method      string
		reqTarget   string
		wantPath    string
		wantEscaped string
		wantQuery   string
	}{
		{http.MethodGet, "/cpamp-api/v1/models", "/v1/models", "/v1/models", ""},
		{http.MethodGet, "/cpamp-api/v1/models/org%2Fgpt-4o?include=meta", "/v1/models/org/gpt-4o", "/v1/models/org%2Fgpt-4o", "include=meta"},
		{http.MethodPost, "/cpamp-api/v1/chat/completions", "/v1/chat/completions", "/v1/chat/completions", ""},
		{http.MethodPost, "/cpamp-api/v1/completions", "/v1/completions", "/v1/completions", ""},
		{http.MethodPost, "/cpamp-api/v1/responses", "/v1/responses", "/v1/responses", ""},
		{http.MethodPost, "/cpamp-api/v1/responses/compact", "/v1/responses/compact", "/v1/responses/compact", ""},
		{http.MethodPost, "/cpamp-api/v1/messages", "/v1/messages", "/v1/messages", ""},
		{http.MethodPost, "/cpamp-api/v1/messages/count_tokens", "/v1/messages/count_tokens", "/v1/messages/count_tokens", ""},
		{http.MethodPost, "/cpamp-api/v1/images/generations", "/v1/images/generations", "/v1/images/generations", ""},
		{http.MethodPost, "/cpamp-api/v1/audio/speech", "/v1/audio/speech", "/v1/audio/speech", ""},
		{http.MethodGet, "/cpamp-api/v1beta/models", "/v1beta/models", "/v1beta/models", ""},
		{http.MethodPost, "/cpamp-api/v1beta/models/gemini-2.5-pro:generateContent?key=client", "/v1beta/models/gemini-2.5-pro:generateContent", "/v1beta/models/gemini-2.5-pro:generateContent", "key=client"},
		{http.MethodPost, "/cpamp-api/v1beta/interactions", "/v1beta/interactions", "/v1beta/interactions", ""},
		{http.MethodPost, "/cpamp-api/openai/v1/videos", "/openai/v1/videos", "/openai/v1/videos", ""},
		{http.MethodGet, "/cpamp-api/openai/v1/videos/vid_1/content", "/openai/v1/videos/vid_1/content", "/openai/v1/videos/vid_1/content", ""},
		{http.MethodPost, "/cpamp-api/backend-api/codex/responses", "/backend-api/codex/responses", "/backend-api/codex/responses", ""},
		{http.MethodGet, "/cpamp-api/backend-api/codex/alpha/search?q=go", "/backend-api/codex/alpha/search", "/backend-api/codex/alpha/search", "q=go"},
		{http.MethodPost, "/cpamp-api/api/provider/openai/v1/chat/completions", "/api/provider/openai/v1/chat/completions", "/api/provider/openai/v1/chat/completions", ""},
	}
	for _, tc := range supportedCases {
		req, err := http.NewRequest(tc.method, public.URL+tc.reqTarget, strings.NewReader("req-body"))
		if err != nil {
			t.Fatalf("new request %s: %v", tc.reqTarget, err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do %s: %v", tc.reqTarget, err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK || res.Header.Get("X-CPAMP-Route-Owner") != "gateway" {
			t.Fatalf("%s status=%d owner=%q, want 200 gateway", tc.reqTarget, res.StatusCode, res.Header.Get("X-CPAMP-Route-Owner"))
		}
		if got := lastGatewayPath.Load().(string); got != tc.wantPath {
			t.Fatalf("%s upstream Path = %q, want %q", tc.reqTarget, got, tc.wantPath)
		}
		if got := lastGatewayEscaped.Load().(string); got != tc.wantEscaped {
			t.Fatalf("%s upstream EscapedPath = %q, want %q", tc.reqTarget, got, tc.wantEscaped)
		}
		if got := lastGatewayQuery.Load().(string); got != tc.wantQuery {
			t.Fatalf("%s upstream RawQuery = %q, want %q", tc.reqTarget, got, tc.wantQuery)
		}
	}

	// 2. SSE streaming and WebSocket upgrade preserve stripped protocol paths.
	sseRes, err := http.Get(public.URL + "/cpamp-api/v1/responses?stream=sse")
	if err != nil {
		t.Fatalf("SSE get: %v", err)
	}
	sseBytes, _ := io.ReadAll(sseRes.Body)
	_ = sseRes.Body.Close()
	if sseRes.StatusCode != http.StatusOK || !strings.Contains(string(sseBytes), "chunk-1") || !strings.Contains(string(sseBytes), "chunk-2") {
		t.Fatalf("SSE response unexpected: status=%d body=%q", sseRes.StatusCode, string(sseBytes))
	}

	for _, wsTarget := range [][2]string{
		{"/cpamp-api/v1/responses", "/v1/responses"},
		{"/cpamp-api/v1/realtime", "/v1/realtime"},
		{"/cpamp-api/backend-api/codex/responses", "/backend-api/codex/responses"},
	} {
		hostAddr := strings.TrimPrefix(public.URL, "http://")
		conn, dialErr := net.Dial("tcp", hostAddr)
		if dialErr != nil {
			t.Fatalf("dial %s: %v", hostAddr, dialErr)
		}
		_, _ = io.WriteString(conn, "GET "+wsTarget[0]+" HTTP/1.1\r\nHost: "+hostAddr+"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		reader := bufio.NewReader(conn)
		resp, readErr := http.ReadResponse(reader, nil)
		if readErr != nil {
			_ = conn.Close()
			t.Fatalf("read WS response for %s: %v", wsTarget[0], readErr)
		}
		if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("X-Upstream-WS-Path") != wsTarget[1] {
			_ = conn.Close()
			t.Fatalf("WS %s status=%d upstreamPath=%q, want 101 %q", wsTarget[0], resp.StatusCode, resp.Header.Get("X-Upstream-WS-Path"), wsTarget[1])
		}
		_ = conn.Close()
	}

	// 3. Unprefixed protocol paths and blocked CPA management/callback paths fail closed (404) without hitting Gateway.
	beforeGatewayHits := gatewayHits.Load()
	for _, rejected := range []string{
		"/v1/models",
		"/v1/chat/completions",
		"/v1/responses",
		"/v1/messages",
		"/v1beta/models",
		"/openai/v1/videos",
		"/backend-api/codex/responses",
		"/api/provider/openai/v1/models",
		"/v0/management/config",
		"/v8/management/config",
		"/cpamp-api",
		"/cpamp-api/",
		"/cpamp-api/v0/management/config",
		"/cpamp-api/v8/management/config",
		"/cpamp-api/v1/runtime/private-configuration",
		"/cpamp-api/management.html",
		"/cpamp-api/healthz",
		"/cpamp-api/keepalive",
		"/cpamp-api/codex/callback",
		"/cpamp-api/devin/callback",
		"/cpamp-api/.well-known/cpamp-bootstrap",
		"/cpamp-api/%2e%2e/v0/management/config",
		"/cpamp-api/v1/%2e%2e/v0/management/config",
		"/cpamp-api/api/provider/openai/v8/management",
	} {
		res, reqErr := http.Get(public.URL + rejected)
		if reqErr != nil {
			t.Fatalf("GET %s: %v", rejected, reqErr)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("rejected path %s status = %d, want 404", rejected, res.StatusCode)
		}
	}
	if gatewayHits.Load() != beforeGatewayHits {
		t.Fatalf("rejected unprefixed or management requests reached Gateway: %d -> %d", beforeGatewayHits, gatewayHits.Load())
	}

	// 4. Control prefix reaches Manager unchanged.
	ctrlRes, err := http.Get(public.URL + "/cpamp-admin/api/v2/operation")
	if err != nil {
		t.Fatalf("GET control: %v", err)
	}
	ctrlBody, _ := io.ReadAll(ctrlRes.Body)
	_ = ctrlRes.Body.Close()
	if ctrlRes.StatusCode != http.StatusOK || string(ctrlBody) != "manager:/cpamp-admin/api/v2/operation" {
		t.Fatalf("control response = %d %q", ctrlRes.StatusCode, string(ctrlBody))
	}
}

func TestSharedEntryDisambiguatesControlAndInferenceAndBlocksManagement(t *testing.T) {
	var managerHits atomic.Int32
	var gatewayHits atomic.Int32

	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		managerHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "manager:"+r.URL.Path)
	}))
	defer manager.Close()

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "gateway:"+r.URL.Path)
	}))
	defer gateway.Close()

	routes := &FreshRoutes{}
	// Identical Target and InferenceTarget without SharedEntry must be rejected by ControlHandler (400).
	applyControlState(t, routes, FreshRouteState{
		InstallationID:     "i",
		Generation:         "g",
		OperationID:        "op",
		Revision:           1,
		Target:             "/shared",
		InferenceTarget:    "/shared",
		SharedEntry:        false,
		Committed:          true,
		InferenceCommitted: true,
	}, http.StatusBadRequest)

	// Confirmed SharedEntry succeeds.
	applyControlState(t, routes, FreshRouteState{
		InstallationID:     "i",
		Generation:         "g",
		OperationID:        "op",
		Revision:           1,
		Target:             "/shared",
		InferenceTarget:    "/shared",
		SharedEntry:        true,
		Committed:          true,
		InferenceCommitted: true,
	}, http.StatusOK)

	h := NewFreshHandler(routes, mustURL(t, manager.URL), mustURL(t, gateway.URL), log.New(io.Discard, "", 0), nil)
	h.EnableFreshGatewayAdmission(func(context.Context) bool { return true })
	public := httptest.NewServer(h)
	defer public.Close()

	for _, mgrPath := range []string{"/shared", "/shared/assets/index.js", "/shared/api/v2/operation", "/shared/keys"} {
		res, err := http.Get(public.URL + mgrPath)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK || res.Header.Get("X-CPAMP-Route-Owner") != "manager" || !strings.HasPrefix(string(body), "manager:") {
			t.Fatalf("shared manager path %s -> %d %q", mgrPath, res.StatusCode, string(body))
		}
	}

	for _, gwPath := range [][2]string{
		{"/shared/v1/models", "gateway:/v1/models"},
		{"/shared/v1/chat/completions", "gateway:/v1/chat/completions"},
		{"/shared/v1beta/models/gemini-2.5-pro:generateContent", "gateway:/v1beta/models/gemini-2.5-pro:generateContent"},
	} {
		res, err := http.Get(public.URL + gwPath[0])
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK || res.Header.Get("X-CPAMP-Route-Owner") != "gateway" || string(body) != gwPath[1] {
			t.Fatalf("shared gateway path %s -> %d %q, want %q", gwPath[0], res.StatusCode, string(body), gwPath[1])
		}
	}

	beforeMgr := managerHits.Load()
	beforeGw := gatewayHits.Load()
	for _, blocked := range []string{
		"/shared/v0/management/config",
		"/shared/v8/management/config",
		"/shared/management.html",
		"/shared/codex/callback",
		"/v1/models",
	} {
		res, err := http.Get(public.URL + blocked)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("shared blocked path %s status = %d, want 404", blocked, res.StatusCode)
		}
	}
	if managerHits.Load() != beforeMgr || gatewayHits.Load() != beforeGw {
		t.Fatalf("blocked paths reached upstream: mgr %d->%d, gw %d->%d", beforeMgr, managerHits.Load(), beforeGw, gatewayHits.Load())
	}
}

func TestOneTimeMissingInferenceTransitionKeepsTargetImmutableAndBlocksSecondChange(t *testing.T) {
	routes := &FreshRoutes{}
	// Existing installed v2 state: Target committed, InferenceTarget empty.
	applyControlState(t, routes, FreshRouteState{
		InstallationID: "i",
		Generation:     "g",
		OperationID:    "op",
		Revision:       1,
		Target:         "/console",
		Committed:      true,
	}, http.StatusOK)

	// One-time inference setup stages InferenceTarget on monotonic revision, then commits.
	applyControlState(t, routes, FreshRouteState{
		InstallationID:     "i",
		Generation:         "g",
		OperationID:        "op",
		Revision:           2,
		Target:             "/console",
		InferenceTarget:    "/cpamp-api",
		Committed:          true,
		InferenceCommitted: false,
	}, http.StatusOK)
	applyControlState(t, routes, FreshRouteState{
		InstallationID:     "i",
		Generation:         "g",
		OperationID:        "op",
		Revision:           2,
		Target:             "/console",
		InferenceTarget:    "/cpamp-api",
		Committed:          true,
		InferenceCommitted: true,
	}, http.StatusOK)

	// Any later attempt to change InferenceTarget or roll back InferenceCommitted is rejected (409).
	applyControlState(t, routes, FreshRouteState{
		InstallationID:     "i",
		Generation:         "g",
		OperationID:        "op",
		Revision:           3,
		Target:             "/console",
		InferenceTarget:    "/another-api",
		Committed:          true,
		InferenceCommitted: true,
	}, http.StatusConflict)
	applyControlState(t, routes, FreshRouteState{
		InstallationID:     "i",
		Generation:         "g",
		OperationID:        "op",
		Revision:           2,
		Target:             "/console",
		InferenceTarget:    "/cpamp-api",
		Committed:          true,
		InferenceCommitted: false,
	}, http.StatusConflict)
}
