package requestbody

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
)

func TestReaderUnblocksStalledBodyOnContextCancelAndIdleTimeout(t *testing.T) {
	t.Run("context_cancel_unblocks_mid_body_read_without_disturbing_completed_reads", func(t *testing.T) {
		reqCtx, cancelReq := context.WithCancel(context.Background())
		defer cancelReq()

		firstChunkRead := make(chan struct{})
		readErrCh := make(chan error, 1)
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, stopBody := Wrap(reqCtx, r.Body, w, 0)
				defer stopBody()
				defer func() { _ = body.Close() }()
				buf := make([]byte, 8)
				if _, rErr := io.ReadFull(body, buf); rErr != nil {
					readErrCh <- fmt.Errorf("initial ReadFull: %w", rErr)
					return
				}
				close(firstChunkRead)
				_, rErr := io.ReadAll(body)
				readErrCh <- rErr
			}),
		}
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(func() { _ = server.Close() })

		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })

		rawReq := "PUT /upload HTTP/1.1\r\nHost: example\r\nContent-Length: 4096\r\n\r\n12345678"
		if _, err := conn.Write([]byte(rawReq)); err != nil {
			t.Fatal(err)
		}
		<-firstChunkRead

		cancelReq()
		select {
		case rErr := <-readErrCh:
			if !errors.Is(rErr, context.Canceled) {
				t.Fatalf("stalled ReadAll after cancel = %v, want context.Canceled", rErr)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("stalled HTTP body read did not unblock on context cancellation")
		}
	})

	t.Run("idle_timeout_bounds_stalled_body_when_explicitly_configured", func(t *testing.T) {
		readErrCh := make(chan error, 1)
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, stopBody := Wrap(r.Context(), r.Body, w, 40*time.Millisecond)
				defer stopBody()
				defer func() { _ = body.Close() }()
				_, rErr := io.ReadAll(body)
				readErrCh <- rErr
			}),
		}
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(func() { _ = server.Close() })

		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })

		rawReq := "PUT /upload HTTP/1.1\r\nHost: example\r\nContent-Length: 1024\r\n\r\nabcd"
		if _, err := conn.Write([]byte(rawReq)); err != nil {
			t.Fatal(err)
		}
		select {
		case rErr := <-readErrCh:
			if rErr == nil {
				t.Fatal("expected read timeout error on stalled body, got nil")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("stalled HTTP body read did not fail within idle timeout")
		}
	})

	t.Run("context_cancel_interrupts_active_non_eof_body_close_drain", func(t *testing.T) {
		reqCtx, cancelReq := context.WithCancel(context.Background())
		defer cancelReq()

		closeEntered := make(chan struct{})
		handlerDone := make(chan struct{})
		var enterOnce sync.Once
		reqCtx = WithEarlyCloseDrainTimeout(reqCtx, -1) // disable timer cap to prove ctx cancel itself unblocks Close
		reqCtx = WithNonEOFCloseObserver(reqCtx, func() {
			enterOnce.Do(func() { close(closeEntered) })
		})

		const limit = 32
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(handlerDone)
				body, stopBody := Wrap(reqCtx, r.Body, w, 0)
				defer stopBody()
				raw, rErr := io.ReadAll(io.LimitReader(body, limit+1))
				if rErr != nil || len(raw) != limit+1 {
					t.Errorf("LimitReader = (%d, %v), want (%d, nil)", len(raw), rErr, limit+1)
					return
				}
				_ = body.Close()
				w.Header().Set("Connection", "close")
				w.WriteHeader(http.StatusRequestEntityTooLarge)
			}),
		}
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(func() { _ = server.Close() })

		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })

		sent := strings.Repeat("x", limit+1)
		rawReq := fmt.Sprintf("POST /limit HTTP/1.1\r\nHost: example\r\nContent-Length: %d\r\n\r\n%s", limit+2, sent)
		if _, err := conn.Write([]byte(rawReq)); err != nil {
			t.Fatal(err)
		}
		<-closeEntered

		cancelReq()
		select {
		case <-handlerDone:
		case <-time.After(2 * time.Second):
			t.Fatal("non-EOF Reader.Close() drain did not unblock on context cancellation")
		}
	})
}

func TestPostEOFExtraBodyReadDoesNotCancelRequestContextOnLiveHandler(t *testing.T) {
	const idleTimeout = 30 * time.Millisecond
	handlerCtxErrCh := make(chan error, 1)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handler := middleware.RequestLogger(Middleware(idleTimeout, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			handlerCtxErrCh <- fmt.Errorf("ReadAll: %w", readErr)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if string(bodyBytes) != "hello-post-eof" {
			handlerCtxErrCh <- fmt.Errorf("body = %q, want hello-post-eof", string(bodyBytes))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Legal extra Read after io.EOF: must short-circuit without setting a new
		// socket read deadline on net/http's backgroundRead watcher.
		var extra [8]byte
		if n, extraErr := r.Body.Read(extra[:]); n != 0 || !errors.Is(extraErr, io.EOF) {
			handlerCtxErrCh <- fmt.Errorf("extra Read after EOF = (%d, %v), want (0, io.EOF)", n, extraErr)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Continue running well past 4x idleTimeout; r.Context() must remain live.
		timer := time.NewTimer(4 * idleTimeout)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			handlerCtxErrCh <- r.Context().Err()
			w.WriteHeader(http.StatusRequestTimeout)
			return
		case <-timer.C:
		}
		handlerCtxErrCh <- r.Context().Err()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})))
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	req, err := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/post-eof", strings.NewReader("hello-post-eof"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do(POST /post-eof): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ctxErr := <-handlerCtxErrCh; ctxErr != nil {
		t.Fatalf("r.Context().Err() after post-EOF Read and 4x idleTimeout = %v, want nil", ctxErr)
	}
}

func TestHTTP11KeepAliveReusesSingleTCPConnAcrossSequentialPosts(t *testing.T) {
	var (
		mu          sync.Mutex
		remoteAddrs []string
		bodies      []string
	)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handler := middleware.RequestLogger(Middleware(DefaultReadIdleTimeout, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, rErr := io.ReadAll(r.Body)
		if rErr != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		remoteAddrs = append(remoteAddrs, r.RemoteAddr)
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ack:" + string(raw)))
	})))
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	// Dial a single raw TCP connection and send two sequential HTTP/1.1 POSTs.
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	br := bufio.NewReader(conn)

	for i, payload := range []string{"first-keepalive-post", "second-keepalive-post"} {
		rawReq := fmt.Sprintf(
			"POST /seq HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\n\r\n%s",
			listener.Addr().String(),
			len(payload),
			payload,
		)
		if _, err := conn.Write([]byte(rawReq)); err != nil {
			t.Fatalf("POST #%d write: %v", i+1, err)
		}
		reqForParse, _ := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/seq", nil)
		resp, err := http.ReadResponse(br, reqForParse)
		if err != nil {
			t.Fatalf("POST #%d ReadResponse: %v", i+1, err)
		}
		respBody, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("POST #%d read body: %v", i+1, err)
		}
		if resp.StatusCode != http.StatusOK || string(respBody) != "ack:"+payload {
			t.Fatalf("POST #%d = (%d, %q), want (200, %q)", i+1, resp.StatusCode, string(respBody), "ack:"+payload)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(remoteAddrs) != 2 || remoteAddrs[0] != remoteAddrs[1] {
		t.Fatalf("server RemoteAddrs across sequential POSTs = %v, want 2 identical addrs on one TCP conn", remoteAddrs)
	}
	if len(bodies) != 2 || bodies[0] != "first-keepalive-post" || bodies[1] != "second-keepalive-post" {
		t.Fatalf("recorded bodies = %v", bodies)
	}
}

// TestSlowMultiChunkStreamingSucceedsNormallyAndAbortsOnCancel is a real-TCP
// chunked-streaming smoke check; TestDefaultMiddlewareSetsNoReadDeadlineUntilContextCancel
// below enforces the exact zero-default-deadline invariant without wall-clock sleeps.
func TestSlowMultiChunkStreamingSucceedsNormallyAndAbortsOnCancel(t *testing.T) {
	const referenceThreshold = 30 * time.Millisecond

	t.Run("normal_operation_allows_inter_chunk_pauses_beyond_threshold", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		handler := middleware.RequestLogger(Middleware(DefaultReadIdleTimeout, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, rErr := io.ReadAll(r.Body)
			if rErr != nil {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(raw)
		})))
		server := &http.Server{Handler: handler}
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(func() { _ = server.Close() })

		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })

		header := fmt.Sprintf("POST /stream HTTP/1.1\r\nHost: %s\r\nTransfer-Encoding: chunked\r\n\r\n", listener.Addr().String())
		if _, err := conn.Write([]byte(header + "6\r\nchunk1\r\n")); err != nil {
			t.Fatal(err)
		}
		// Pause 4x referenceThreshold before sending the second chunk.
		pause := time.NewTimer(4 * referenceThreshold)
		<-pause.C
		if _, err := conn.Write([]byte("6\r\nchunk2\r\n0\r\n\r\n")); err != nil {
			t.Fatal(err)
		}

		reqForParse, _ := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/stream", nil)
		resp, err := http.ReadResponse(bufio.NewReader(conn), reqForParse)
		if err != nil {
			t.Fatalf("ReadResponse: %v", err)
		}
		got, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK || string(got) != "chunk1chunk2" {
			t.Fatalf("slow chunked upload = (%d, %q, %v), want (200, \"chunk1chunk2\", nil)", resp.StatusCode, string(got), err)
		}
	})

	t.Run("context_cancellation_aborts_active_chunk_wait_immediately", func(t *testing.T) {
		reqCtx, cancelReq := context.WithCancel(context.Background())
		defer cancelReq()

		secondReadAttempt := make(chan struct{})
		readErrCh := make(chan error, 1)
		var once sync.Once
		reqCtx = WithReadObserver(reqCtx, func(attempt int) {
			if attempt >= 2 {
				once.Do(func() { close(secondReadAttempt) })
			}
		})

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, stopBody := Wrap(reqCtx, r.Body, w, DefaultReadIdleTimeout)
				defer stopBody()
				defer func() { _ = body.Close() }()
				_, rErr := io.ReadAll(body)
				readErrCh <- rErr
			}),
		}
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(func() { _ = server.Close() })

		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })

		header := fmt.Sprintf("POST /stream HTTP/1.1\r\nHost: %s\r\nTransfer-Encoding: chunked\r\n\r\n6\r\nchunk1\r\n", listener.Addr().String())
		if _, err := conn.Write([]byte(header)); err != nil {
			t.Fatal(err)
		}
		<-secondReadAttempt

		cancelReq()
		select {
		case rErr := <-readErrCh:
			if !errors.Is(rErr, context.Canceled) {
				t.Fatalf("aborted chunk read = %v, want context.Canceled", rErr)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("active chunk wait did not abort immediately on context cancel")
		}
	})
}

func TestLargeResponseWithUnreadBodyPreservesKeepAliveWhenDrainedAndAbortsWhenStalled(t *testing.T) {
	largeAsset := strings.Repeat("F", 5542) // > 2048 B net/http bufio.Writer flush threshold

	t.Run("small_unread_body_arriving_within_drain_window_reuses_keep_alive_conn", func(t *testing.T) {
		var (
			mu          sync.Mutex
			remoteAddrs []string
		)
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		handler := middleware.RequestLogger(Middleware(DefaultReadIdleTimeout, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Intentionally do NOT read r.Body before writing >2048 bytes.
			mu.Lock()
			remoteAddrs = append(remoteAddrs, r.RemoteAddr)
			mu.Unlock()
			w.Header().Set("Content-Type", "image/x-icon")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(largeAsset))
		})))
		server := &http.Server{Handler: handler}
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(func() { _ = server.Close() })

		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		br := bufio.NewReader(conn)

		// Request #1 includes a 16-byte body that the handler ignores.
		req1 := fmt.Sprintf("GET /favicon.ico HTTP/1.1\r\nHost: %s\r\nContent-Length: 16\r\n\r\n0123456789abcdef", listener.Addr().String())
		if _, err := conn.Write([]byte(req1)); err != nil {
			t.Fatal(err)
		}
		parseReq, _ := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+"/favicon.ico", nil)
		resp1, err := http.ReadResponse(br, parseReq)
		if err != nil {
			t.Fatalf("GET #1 ReadResponse: %v", err)
		}
		body1, err := io.ReadAll(resp1.Body)
		_ = resp1.Body.Close()
		if err != nil || resp1.StatusCode != http.StatusOK || len(body1) != len(largeAsset) {
			t.Fatalf("GET #1 = (%d, %d, %v), want (200, %d, nil)", resp1.StatusCode, len(body1), err, len(largeAsset))
		}

		// Request #2 reuses the exact same HTTP/1.1 TCP connection.
		req2 := fmt.Sprintf("GET /favicon.ico HTTP/1.1\r\nHost: %s\r\n\r\n", listener.Addr().String())
		if _, err := conn.Write([]byte(req2)); err != nil {
			t.Fatal(err)
		}
		resp2, err := http.ReadResponse(br, parseReq)
		if err != nil {
			t.Fatalf("GET #2 ReadResponse: %v", err)
		}
		body2, err := io.ReadAll(resp2.Body)
		_ = resp2.Body.Close()
		if err != nil || resp2.StatusCode != http.StatusOK || len(body2) != len(largeAsset) {
			t.Fatalf("GET #2 = (%d, %d, %v), want (200, %d, nil)", resp2.StatusCode, len(body2), err, len(largeAsset))
		}

		mu.Lock()
		defer mu.Unlock()
		if len(remoteAddrs) != 2 || remoteAddrs[0] != remoteAddrs[1] {
			t.Fatalf("RemoteAddrs across keep-alive GETs = %v, want 2 identical addrs", remoteAddrs)
		}
	})

	t.Run("stalled_unread_body_on_large_response_aborts_on_context_cancel", func(t *testing.T) {
		reqCtx, cancelReq := context.WithCancel(context.Background())
		defer cancelReq()

		closeEntered := make(chan struct{})
		handlerDone := make(chan struct{})
		var once sync.Once
		reqCtx = WithEarlyCloseDrainTimeout(reqCtx, -1)
		reqCtx = WithNonEOFCloseObserver(reqCtx, func() {
			once.Do(func() { close(closeEntered) })
		})

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(handlerDone)
				body, stopBody := Wrap(reqCtx, r.Body, w, 0)
				defer stopBody()
				defer func() { _ = body.Close() }()
				w.Header().Set("Content-Type", "image/x-icon")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(largeAsset))
			}),
		}
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(func() { _ = server.Close() })

		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })

		rawReq := fmt.Sprintf("GET /favicon.ico HTTP/1.1\r\nHost: %s\r\nContent-Length: 16\r\n\r\n", listener.Addr().String())
		if _, err := conn.Write([]byte(rawReq)); err != nil {
			t.Fatal(err)
		}
		select {
		case <-closeEntered:
		case <-time.After(2 * time.Second):
			t.Fatal("large response handler blocked before reaching deferred Reader.Close()")
		}

		cancelReq()
		select {
		case <-handlerDone:
		case <-time.After(2 * time.Second):
			t.Fatal("deferred Reader.Close() did not unblock on context cancel")
		}
	})
}

type deadlineRecordingResponseWriter struct {
	header     http.Header
	status     int
	mu         sync.Mutex
	deadlines  []time.Time
	fullDuplex bool
	onDeadline func(time.Time)
}

func newDeadlineRecordingResponseWriter(onDeadline func(time.Time)) *deadlineRecordingResponseWriter {
	return &deadlineRecordingResponseWriter{
		header:     make(http.Header),
		status:     http.StatusOK,
		onDeadline: onDeadline,
	}
}

func (w *deadlineRecordingResponseWriter) Header() http.Header { return w.header }

func (w *deadlineRecordingResponseWriter) Write(p []byte) (int, error) { return len(p), nil }

func (w *deadlineRecordingResponseWriter) WriteHeader(status int) { w.status = status }

func (w *deadlineRecordingResponseWriter) EnableFullDuplex() error {
	w.mu.Lock()
	w.fullDuplex = true
	w.mu.Unlock()
	return nil
}

func (w *deadlineRecordingResponseWriter) SetReadDeadline(d time.Time) error {
	w.mu.Lock()
	w.deadlines = append(w.deadlines, d)
	cb := w.onDeadline
	w.mu.Unlock()
	if cb != nil {
		cb(d)
	}
	return nil
}

func (w *deadlineRecordingResponseWriter) recordedDeadlines() []time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]time.Time, len(w.deadlines))
	copy(out, w.deadlines)
	return out
}

type multiChunkSliceBody struct {
	chunks [][]byte
	idx    int
}

func (b *multiChunkSliceBody) Read(p []byte) (int, error) {
	if b.idx >= len(b.chunks) {
		return 0, io.EOF
	}
	n := copy(p, b.chunks[b.idx])
	b.idx++
	return n, nil
}

func (b *multiChunkSliceBody) Close() error { return nil }

type blockingDeadlineBody struct {
	readEntered     chan struct{}
	deadlineExpired <-chan struct{}
	once            sync.Once
}

func (b *blockingDeadlineBody) Read(_ []byte) (int, error) {
	b.once.Do(func() { close(b.readEntered) })
	<-b.deadlineExpired
	return 0, os.ErrDeadlineExceeded
}

func (b *blockingDeadlineBody) Close() error { return nil }

func TestDefaultMiddlewareSetsNoReadDeadlineUntilContextCancel(t *testing.T) {
	t.Run("normal_reads_through_default_middleware_set_zero_read_deadlines", func(t *testing.T) {
		rw := newDeadlineRecordingResponseWriter(nil)
		var got string
		h := middleware.RequestLogger(Middleware(DefaultReadIdleTimeout, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("io.ReadAll: %v", err)
			}
			got = string(raw)
			w.WriteHeader(http.StatusOK)
		})))
		req, err := http.NewRequest(http.MethodPost, "http://example.invalid/upload", &multiChunkSliceBody{
			chunks: [][]byte{[]byte("chunk-1:"), []byte("chunk-2")},
		})
		if err != nil {
			t.Fatal(err)
		}
		h.ServeHTTP(rw, req)
		if got != "chunk-1:chunk-2" {
			t.Fatalf("body = %q, want %q", got, "chunk-1:chunk-2")
		}
		if deadlines := rw.recordedDeadlines(); len(deadlines) != 0 {
			t.Fatalf("DefaultReadIdleTimeout middleware set read deadlines during normal Read: %v, want none", deadlines)
		}
	})

	t.Run("cancelling_request_context_sets_expired_read_deadline", func(t *testing.T) {
		reqCtx, cancelReq := context.WithCancel(context.Background())
		defer cancelReq()

		deadlineExpired := make(chan struct{})
		var expOnce sync.Once
		rw := newDeadlineRecordingResponseWriter(func(d time.Time) {
			if !d.IsZero() && !d.After(time.Now()) {
				expOnce.Do(func() { close(deadlineExpired) })
			}
		})
		body := &blockingDeadlineBody{
			readEntered:     make(chan struct{}),
			deadlineExpired: deadlineExpired,
		}
		readErrCh := make(chan error, 1)
		h := middleware.RequestLogger(Middleware(DefaultReadIdleTimeout, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			buf := make([]byte, 16)
			_, rErr := r.Body.Read(buf)
			readErrCh <- rErr
			w.WriteHeader(http.StatusRequestTimeout)
		})))
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, "http://example.invalid/stream", body)
		if err != nil {
			t.Fatal(err)
		}
		serveDone := make(chan struct{})
		go func() {
			defer close(serveDone)
			h.ServeHTTP(rw, req)
		}()

		<-body.readEntered
		if deadlines := rw.recordedDeadlines(); len(deadlines) != 0 {
			t.Fatalf("read deadlines set before context cancel = %v, want none", deadlines)
		}

		cancelReq()
		select {
		case rErr := <-readErrCh:
			if !errors.Is(rErr, context.Canceled) {
				t.Fatalf("canceled Read err = %v, want context.Canceled", rErr)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Read did not abort on context cancellation")
		}
		<-serveDone
		deadlines := rw.recordedDeadlines()
		if len(deadlines) == 0 || deadlines[0].IsZero() || deadlines[0].After(time.Now()) {
			t.Fatalf("recorded deadlines after cancel = %v, want immediate expired deadline", deadlines)
		}
	})
}

type blockingCloseBody struct {
	inInnerClose    chan struct{}
	deadlineExpired <-chan struct{}
	once            sync.Once
}

func (b *blockingCloseBody) Read(_ []byte) (int, error) {
	return 0, os.ErrDeadlineExceeded
}

func (b *blockingCloseBody) Close() error {
	b.once.Do(func() { close(b.inInnerClose) })
	<-b.deadlineExpired
	return os.ErrDeadlineExceeded
}

func TestConcurrentReaderCloseKeepsCancelWatcherArmedUntilDrainFinishes(t *testing.T) {
	t.Run("second_concurrent_close_waits_and_keeps_cancel_watcher_active", func(t *testing.T) {
		reqCtx, cancelReq := context.WithCancel(context.Background())
		defer cancelReq()
		reqCtx = WithEarlyCloseDrainTimeout(reqCtx, -1)

		deadlineExpired := make(chan struct{})
		var expOnce sync.Once
		rw := newDeadlineRecordingResponseWriter(func(d time.Time) {
			if !d.IsZero() && !d.After(time.Now()) {
				expOnce.Do(func() { close(deadlineExpired) })
			}
		})
		inner := &blockingCloseBody{
			inInnerClose:    make(chan struct{}),
			deadlineExpired: deadlineExpired,
		}
		wrapped, stopWrap := Wrap(reqCtx, inner, rw, DefaultReadIdleTimeout)
		defer stopWrap()

		firstErrCh := make(chan error, 1)
		go func() {
			firstErrCh <- wrapped.Close()
		}()
		<-inner.inInnerClose

		secondStarted := make(chan struct{})
		secondErrCh := make(chan error, 1)
		go func() {
			close(secondStarted)
			secondErrCh <- wrapped.Close()
		}()
		<-secondStarted
		for range 100 {
			runtime.Gosched()
		}
		select {
		case err := <-secondErrCh:
			// Unblock inner so the leaked goroutine from the unfixed path exits cleanly.
			expOnce.Do(func() { close(deadlineExpired) })
			t.Fatalf("second concurrent Close() returned (%v) while inner.Close() was still draining", err)
		default:
		}

		cancelReq()
		select {
		case err1 := <-firstErrCh:
			select {
			case err2 := <-secondErrCh:
				if !errors.Is(err1, os.ErrDeadlineExceeded) || !errors.Is(err2, os.ErrDeadlineExceeded) {
					t.Fatalf("Close errors = (%v, %v), want both os.ErrDeadlineExceeded", err1, err2)
				}
			case <-time.After(time.Second):
				t.Fatal("second concurrent Close() did not unblock after first Close() finished")
			}
		case <-time.After(time.Second):
			expOnce.Do(func() { close(deadlineExpired) })
			t.Fatal("first Close() did not unblock on context cancel after concurrent Close()")
		}
	})

	t.Run("real_tcp_concurrent_close_aborts_promptly_on_cancel", func(t *testing.T) {
		reqCtx, cancelReq := context.WithCancel(context.Background())
		defer cancelReq()
		reqCtx = WithEarlyCloseDrainTimeout(reqCtx, -1)

		firstCloseEntered := make(chan struct{})
		var once sync.Once
		reqCtx = WithNonEOFCloseObserver(reqCtx, func() {
			once.Do(func() { close(firstCloseEntered) })
		})

		bothClosed := make(chan struct{})
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, stopBody := Wrap(reqCtx, r.Body, w, DefaultReadIdleTimeout)
				defer stopBody()

				var wg sync.WaitGroup
				wg.Add(2)
				go func() {
					defer wg.Done()
					_ = body.Close()
				}()
				<-firstCloseEntered
				go func() {
					defer wg.Done()
					_ = body.Close()
				}()
				wg.Wait()
				close(bothClosed)
				w.WriteHeader(http.StatusRequestEntityTooLarge)
			}),
		}
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(func() { _ = server.Close() })

		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })

		rawReq := fmt.Sprintf("POST /upload HTTP/1.1\r\nHost: %s\r\nContent-Length: 16\r\n\r\n", listener.Addr().String())
		if _, err := conn.Write([]byte(rawReq)); err != nil {
			t.Fatal(err)
		}
		<-firstCloseEntered
		for range 100 {
			runtime.Gosched()
		}
		cancelReq()

		select {
		case <-bothClosed:
		case <-time.After(2 * time.Second):
			t.Fatal("real-TCP concurrent Close() did not unblock on context cancel")
		}
	})
}

func TestReaderCloseSkipsDrainWhenHandlerSetsConnectionClose(t *testing.T) {
	t.Run("unit_close_preserves_expired_deadline_without_clearing", func(t *testing.T) {
		rw := newDeadlineRecordingResponseWriter(nil)
		rw.Header().Set("Connection", "keep-alive, close")
		inner := io.NopCloser(strings.NewReader("remaining-unread-bytes"))
		wrapped, stopWrap := Wrap(context.Background(), inner, rw, DefaultReadIdleTimeout)
		defer stopWrap()

		if err := wrapped.Close(); err != nil {
			t.Fatalf("Close() = %v, want nil", err)
		}
		if got := rw.Header().Get("Connection"); got != "close" {
			t.Fatalf("Connection header = %q, want normalized \"close\"", got)
		}
		deadlines := rw.recordedDeadlines()
		if len(deadlines) != 1 || deadlines[0].IsZero() || deadlines[0].After(time.Now()) {
			t.Fatalf("recorded deadlines = %v, want single immediate expired deadline without trailing zero reset", deadlines)
		}
	})

	t.Run("real_tcp_early_connection_close_returns_without_drain_timeout_stall", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		handler := middleware.RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := WithEarlyCloseDrainTimeout(r.Context(), 5*time.Second)
			body, stopBody := Wrap(ctx, r.Body, w, DefaultReadIdleTimeout)
			defer stopBody()
			defer func() { _ = body.Close() }()
			r.Body = body
			w.Header().Set("Connection", "close")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_, _ = w.Write([]byte("too large"))
		}))
		server := &http.Server{Handler: handler}
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(func() { _ = server.Close() })

		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })

		started := time.Now()
		rawReq := fmt.Sprintf("POST /upload HTTP/1.1\r\nHost: %s\r\nContent-Length: 4096\r\n\r\npartial", listener.Addr().String())
		if _, err := conn.Write([]byte(rawReq)); err != nil {
			t.Fatal(err)
		}
		parseReq, _ := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/upload", nil)
		br := bufio.NewReader(conn)
		resp, err := http.ReadResponse(br, parseReq)
		if err != nil {
			t.Fatalf("ReadResponse: %v", err)
		}
		respBody, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("read response body: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		var extra [1]byte
		n, closeErr := br.Read(extra[:])
		elapsed := time.Since(started)

		if elapsed >= 500*time.Millisecond {
			t.Fatalf("Connection: close response + socket close took %v, want immediate close without 5s drain stall", elapsed)
		}
		if resp.StatusCode != http.StatusRequestEntityTooLarge || !resp.Close || string(respBody) != "too large" {
			t.Fatalf("response = (%d, close=%v, %q), want (413, true, \"too large\")", resp.StatusCode, resp.Close, string(respBody))
		}
		if n != 0 || closeErr == nil || errors.Is(closeErr, os.ErrDeadlineExceeded) {
			t.Fatalf("post-response TCP read = (%d, %v), want immediate connection close", n, closeErr)
		}
	})

	t.Run("real_tcp_comma_separated_connection_close_closes_socket_on_http_server", func(t *testing.T) {
		var healthHits atomic.Int32
		mux := http.NewServeMux()
		mux.HandleFunc("/upload", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Connection", "keep-alive, close")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_, _ = w.Write([]byte("too large"))
		})
		mux.HandleFunc("/upload-eof", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			w.Header().Set("Connection", "keep-alive, Close")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("done"))
		})
		mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			healthHits.Add(1)
			w.WriteHeader(http.StatusOK)
		})

		withDrainOverride := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(WithEarlyCloseDrainTimeout(r.Context(), 5*time.Second))
			Middleware(DefaultReadIdleTimeout, mux).ServeHTTP(w, r)
		})
		ts := httptest.NewServer(middleware.RequestLogger(withDrainOverride))
		t.Cleanup(ts.Close)
		addr := ts.Listener.Addr().String()

		t.Run("unread_body_skips_drain_and_closes_tcp", func(t *testing.T) {
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })

			unreadFollowUp := fmt.Sprintf("GET /health HTTP/1.1\r\nHost: %s\r\n\r\n", addr)
			rawReq := fmt.Sprintf("POST /upload HTTP/1.1\r\nHost: %s\r\nContent-Length: 4096\r\n\r\n%s", addr, unreadFollowUp)
			started := time.Now()
			if _, err := conn.Write([]byte(rawReq)); err != nil {
				t.Fatal(err)
			}
			assertHTTP11ResponseAndImmediateClose(t, conn, started, 500*time.Millisecond, http.StatusRequestEntityTooLarge, "too large", true)
			if got := healthHits.Load(); got != 0 {
				t.Fatalf("/health hit count = %d, want 0", got)
			}
		})

		t.Run("eof_body_normalizes_comma_close_and_closes_tcp", func(t *testing.T) {
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })

			rawReq := fmt.Sprintf("POST /upload-eof HTTP/1.1\r\nHost: %s\r\nContent-Length: 7\r\n\r\nallread", addr)
			started := time.Now()
			if _, err := conn.Write([]byte(rawReq)); err != nil {
				t.Fatal(err)
			}
			assertHTTP11ResponseAndImmediateClose(t, conn, started, 500*time.Millisecond, http.StatusOK, "done", true)
		})
	})

	t.Run("real_tcp_post_writeheader_connection_close_drains_small_body_or_closes_on_timeout", func(t *testing.T) {
		var healthHits atomic.Int32
		mux := http.NewServeMux()
		mux.HandleFunc("/late-close", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("committed-before-close-header"))
			// Mutating Header() after WriteHeader does not alter net/http's committed header snapshot.
			w.Header().Set("Connection", "close")
		})
		mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			healthHits.Add(1)
			w.WriteHeader(http.StatusNoContent)
		})

		withShortDrain := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.WithContext(WithEarlyCloseDrainTimeout(r.Context(), 40*time.Millisecond))
			Middleware(DefaultReadIdleTimeout, mux).ServeHTTP(w, r)
		})
		ts := httptest.NewServer(middleware.RequestLogger(withShortDrain))
		t.Cleanup(ts.Close)
		addr := ts.Listener.Addr().String()

		// Small unread body already on the wire is drained instead of leaving bytes on keep-alive conn.
		conn1, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn1.Close() })
		br1 := bufio.NewReader(conn1)
		req1 := fmt.Sprintf("POST /late-close HTTP/1.1\r\nHost: %s\r\nContent-Length: 16\r\n\r\n0123456789abcdef", addr)
		if _, err := conn1.Write([]byte(req1)); err != nil {
			t.Fatal(err)
		}
		parsePost, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/late-close", nil)
		resp1, err := http.ReadResponse(br1, parsePost)
		if err != nil {
			t.Fatalf("ReadResponse #1: %v", err)
		}
		_, _ = io.ReadAll(resp1.Body)
		_ = resp1.Body.Close()

		req2 := fmt.Sprintf("GET /health HTTP/1.1\r\nHost: %s\r\n\r\n", addr)
		if _, err := conn1.Write([]byte(req2)); err != nil {
			t.Fatal(err)
		}
		parseGet, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/health", nil)
		resp2, err := http.ReadResponse(br1, parseGet)
		if err != nil {
			t.Fatalf("keep-alive GET /health after drained body failed: %v", err)
		}
		_ = resp2.Body.Close()
		if resp2.StatusCode != http.StatusNoContent || healthHits.Load() != 1 {
			t.Fatalf("keep-alive follow-up = (%d, hits=%d), want (204, 1)", resp2.StatusCode, healthHits.Load())
		}

		// Stalled unread body exceeding drain timeout forces TCP close without second-request bleed.
		healthHits.Store(0)
		conn2, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn2.Close() })
		stalledReq := fmt.Sprintf("POST /late-close HTTP/1.1\r\nHost: %s\r\nContent-Length: 256\r\n\r\npartial", addr)
		started := time.Now()
		if _, err := conn2.Write([]byte(stalledReq)); err != nil {
			t.Fatal(err)
		}
		assertHTTP11ResponseAndImmediateClose(t, conn2, started, time.Second, http.StatusOK, "committed-before-close-header", false)
		_, _ = conn2.Write([]byte(fmt.Sprintf("GET /health HTTP/1.1\r\nHost: %s\r\n\r\n", addr)))
		if got := healthHits.Load(); got != 0 {
			t.Fatalf("/health hit count after timed-out drain = %d, want 0", got)
		}
	})
}

func TestEarlyUnreadBodyResponsesCloseHTTP11TCPWithoutSecondRequestBleed(t *testing.T) {
	const overDrainThresholdLength = 512 << 10
	flushedAsset := strings.Repeat("A", 5542)

	var healthHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/config", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/unread-small-reply", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("accepted"))
	})
	mux.HandleFunc("/unread-flushed-reply", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(flushedAsset))
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		healthHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})

	var drainTimeoutOverride atomic.Int64
	routes := middleware.RejectCrossSiteAdmin(mux, nil)
	wrapped := middleware.RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if customNS := drainTimeoutOverride.Load(); customNS != 0 {
			r = r.WithContext(WithEarlyCloseDrainTimeout(r.Context(), time.Duration(customNS)))
		}
		Middleware(DefaultReadIdleTimeout, routes).ServeHTTP(w, r)
	}))
	ts := httptest.NewServer(wrapped)
	t.Cleanup(ts.Close)
	addr := ts.Listener.Addr().String()

	t.Run("403_reject_cross_site_admin_with_over_256kib_body_closes_promptly_without_bleed", func(t *testing.T) {
		healthHits.Store(0)
		drainTimeoutOverride.Store(0)

		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })

		followUpLine := fmt.Sprintf("GET /health HTTP/1.1\r\nHost: %s\r\n\r\n", addr)
		rawReq := fmt.Sprintf(
			"POST /admin/config HTTP/1.1\r\nHost: %s\r\nSec-Fetch-Site: cross-site\r\nSec-Fetch-Dest: empty\r\nContent-Length: %d\r\n\r\n%s",
			addr,
			overDrainThresholdLength,
			followUpLine,
		)
		started := time.Now()
		if _, err := conn.Write([]byte(rawReq)); err != nil {
			t.Fatal(err)
		}
		assertHTTP11ResponseAndImmediateClose(t, conn, started, 200*time.Millisecond, http.StatusForbidden, "cross-site admin request rejected", true)
		if got := healthHits.Load(); got != 0 {
			t.Fatalf("/health hit count = %d, want 0", got)
		}
	})

	t.Run("301_non_canonical_servemux_path_with_over_256kib_body_closes_promptly_without_bleed", func(t *testing.T) {
		healthHits.Store(0)
		drainTimeoutOverride.Store(0)

		t.Run("declared_content_length_over_256kib", func(t *testing.T) {
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })

			followUpLine := fmt.Sprintf("GET /health HTTP/1.1\r\nHost: %s\r\n\r\n", addr)
			rawReq := fmt.Sprintf(
				"POST /admin//config HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\n\r\n%s",
				addr,
				overDrainThresholdLength,
				followUpLine,
			)
			started := time.Now()
			if _, err := conn.Write([]byte(rawReq)); err != nil {
				t.Fatal(err)
			}
			resp := assertHTTP11ResponseAndImmediateClose(t, conn, started, 200*time.Millisecond, http.StatusTemporaryRedirect, "", true)
			if got := resp.Header.Get("Location"); got != "/admin/config" {
				t.Fatalf("307 Location = %q, want /admin/config", got)
			}
			if got := healthHits.Load(); got != 0 {
				t.Fatalf("/health hit count = %d, want 0", got)
			}
		})

		t.Run("chunked_stream_over_256kib_restores_orig_body_for_early_close", func(t *testing.T) {
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })

			bigChunk := strings.Repeat("x", 300<<10)
			followUpLine := fmt.Sprintf("GET /health HTTP/1.1\r\nHost: %s\r\n\r\n", addr)
			rawReq := fmt.Sprintf(
				"POST /admin//config HTTP/1.1\r\nHost: %s\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n0\r\n\r\n%s",
				addr,
				len(bigChunk),
				bigChunk,
				followUpLine,
			)
			started := time.Now()
			go func() {
				_, _ = conn.Write([]byte(rawReq))
			}()
			resp := assertHTTP11ResponseAndImmediateClose(t, conn, started, time.Second, http.StatusTemporaryRedirect, "", false)
			if got := resp.Header.Get("Location"); got != "/admin/config" {
				t.Fatalf("307 Location = %q, want /admin/config", got)
			}
			if got := healthHits.Load(); got != 0 {
				t.Fatalf("/health hit count = %d, want 0", got)
			}
		})
	})

	t.Run("under_256kib_slow_unread_body_exceeding_drain_timeout_closes_without_bleed", func(t *testing.T) {
		for _, tc := range []struct {
			name        string
			path        string
			wantStatus  int
			wantSubstr  string
			wantWireHdr bool
		}{
			{
				name:        "unflushed_small_response_marks_connection_close",
				path:        "/unread-small-reply",
				wantStatus:  http.StatusAccepted,
				wantSubstr:  "accepted",
				wantWireHdr: true,
			},
			{
				name:        "already_flushed_large_response_still_closes_socket",
				path:        "/unread-flushed-reply",
				wantStatus:  http.StatusOK,
				wantSubstr:  "AAAA",
				wantWireHdr: false,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				healthHits.Store(0)
				drainTimeoutOverride.Store(int64(40 * time.Millisecond))

				conn, err := net.Dial("tcp", addr)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = conn.Close() })

				followUpLine := fmt.Sprintf("GET /health HTTP/1.1\r\nHost: %s\r\n\r\n", addr)
				rawReq := fmt.Sprintf(
					"POST %s HTTP/1.1\r\nHost: %s\r\nContent-Length: 4096\r\n\r\n%s",
					tc.path,
					addr,
					followUpLine,
				)
				started := time.Now()
				if _, err := conn.Write([]byte(rawReq)); err != nil {
					t.Fatal(err)
				}
				assertHTTP11ResponseAndImmediateClose(t, conn, started, time.Second, tc.wantStatus, tc.wantSubstr, tc.wantWireHdr)
				_, _ = conn.Write([]byte(followUpLine))
				if got := healthHits.Load(); got != 0 {
					t.Fatalf("/health hit count = %d, want 0", got)
				}
			})
		}
	})
}

func assertHTTP11ResponseAndImmediateClose(
	t *testing.T,
	conn net.Conn,
	started time.Time,
	maxElapsed time.Duration,
	wantStatus int,
	wantSubstr string,
	requireWireCloseHeader bool,
) *http.Response {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var rawWire bytes.Buffer
	br := bufio.NewReader(io.TeeReader(conn, &rawWire))
	parseReq, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/", nil)
	resp, err := http.ReadResponse(br, parseReq)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	respBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	var extra [1]byte
	n, closeErr := br.Read(extra[:])
	elapsed := time.Since(started)

	if elapsed >= maxElapsed {
		t.Fatalf("response + TCP close took %v, want under %v", elapsed, maxElapsed)
	}
	if resp.StatusCode != wantStatus || !strings.Contains(string(respBody), wantSubstr) {
		t.Fatalf("response = (%d, %q), want (%d containing %q)", resp.StatusCode, string(respBody), wantStatus, wantSubstr)
	}
	if requireWireCloseHeader {
		if !resp.Close || !strings.Contains(strings.ToLower(rawWire.String()), "\r\nconnection: close\r\n") {
			t.Fatalf("response Close=%v wire=%q, want Connection: close on wire", resp.Close, rawWire.String())
		}
	}
	if n != 0 || closeErr == nil || errors.Is(closeErr, os.ErrDeadlineExceeded) {
		t.Fatalf("post-response TCP read = (%d, %v), want immediate socket close without second response", n, closeErr)
	}
	return resp
}

type eofWithBytesReader struct {
	data      []byte
	cancel    context.CancelFunc
	splitZero bool
	nonEOFErr error
	step      int
}

func (r *eofWithBytesReader) Read(p []byte) (int, error) {
	r.step++
	if r.nonEOFErr != nil {
		if r.cancel != nil {
			r.cancel()
		}
		return 0, r.nonEOFErr
	}
	if r.splitZero {
		if r.step == 1 {
			return copy(p, r.data), nil
		}
		if r.cancel != nil {
			r.cancel()
		}
		return 0, io.EOF
	}
	if r.cancel != nil {
		r.cancel()
	}
	if r.step > 1 {
		return 0, io.EOF
	}
	return copy(p, r.data), io.EOF
}

func (r *eofWithBytesReader) Close() error { return nil }

func TestReaderPreservesEOFWhenContextCancelsConcurrently(t *testing.T) {
	t.Run("single read returning (n, io.EOF) while ctx cancels", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		wrapped, stop := Wrap(ctx, &eofWithBytesReader{data: []byte("abcd"), cancel: cancel}, nil, 0)
		defer stop()
		defer func() { _ = wrapped.Close() }()

		buf := make([]byte, 8)
		n, err := wrapped.Read(buf)
		if n != 4 || !errors.Is(err, io.EOF) {
			t.Fatalf("Read on concurrent ctx cancel with io.EOF = (%d, %v), want (4, io.EOF)", n, err)
		}
		n2, err2 := wrapped.Read(buf)
		if n2 != 0 || !errors.Is(err2, io.EOF) {
			t.Fatalf("subsequent Read after io.EOF = (%d, %v), want (0, io.EOF)", n2, err2)
		}
	})

	t.Run("two-step io.ReadAll returning (0, io.EOF) while ctx cancels", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		wrapped, stop := Wrap(ctx, &eofWithBytesReader{data: []byte("abcd"), cancel: cancel, splitZero: true}, nil, 0)
		defer stop()
		defer func() { _ = wrapped.Close() }()

		got, err := io.ReadAll(wrapped)
		if err != nil || string(got) != "abcd" {
			t.Fatalf("io.ReadAll = (%q, %v), want (\"abcd\", nil)", string(got), err)
		}
	})

	t.Run("non-EOF error with canceled ctx still surfaces context error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		wrapped, stop := Wrap(ctx, &eofWithBytesReader{cancel: cancel, nonEOFErr: os.ErrDeadlineExceeded}, nil, 0)
		defer stop()
		defer func() { _ = wrapped.Close() }()

		buf := make([]byte, 8)
		_, err := wrapped.Read(buf)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Read non-EOF error with canceled ctx = %v, want context.Canceled", err)
		}
	})
}

func TestChunkedOver256KiBUnreadBodyClosesWithoutRestoringOrigBody(t *testing.T) {
	var healthHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/early-no-restore", func(w http.ResponseWriter, r *http.Request) {
		body, stop := Wrap(r.Context(), r.Body, w, 0)
		defer stop()
		// Deliberately do NOT restore r.Body = body; Close() must drain-probe >256KiB and close TCP.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
		_ = body.Close()
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		healthHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	addr := ts.Listener.Addr().String()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	bigChunk := strings.Repeat("z", 300<<10)
	followUp := fmt.Sprintf("GET /health HTTP/1.1\r\nHost: %s\r\n\r\n", addr)
	rawReq := fmt.Sprintf(
		"POST /early-no-restore HTTP/1.1\r\nHost: %s\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n0\r\n\r\n%s",
		addr,
		len(bigChunk),
		bigChunk,
		followUp,
	)
	started := time.Now()
	if _, err := conn.Write([]byte(rawReq)); err != nil {
		t.Fatal(err)
	}
	assertHTTP11ResponseAndImmediateClose(t, conn, started, time.Second, http.StatusOK, "done", true)
	if got := healthHits.Load(); got != 0 {
		t.Fatalf("/health hit count = %d, want 0", got)
	}
}

func TestWrapUnknownLengthSmallBodyCloseClearsTemporaryDrainDeadline(t *testing.T) {
	rw := newDeadlineRecordingResponseWriter(nil)
	body, stop := Wrap(
		context.Background(),
		io.NopCloser(strings.NewReader("small-unknown-length-unread-body")),
		rw,
		0,
	)
	defer stop()

	if err := body.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	deadlines := rw.recordedDeadlines()
	if len(deadlines) != 2 {
		t.Fatalf("recorded deadlines = %v (len=%d), want 2 ([drainDeadline, zero])", deadlines, len(deadlines))
	}
	if deadlines[0].IsZero() {
		t.Fatalf("first deadline = %v, want non-zero temporary drain deadline", deadlines[0])
	}
	if !deadlines[1].IsZero() {
		t.Fatalf("final deadline = %v, want zero time.Time{} after draining unknown-length body to EOF", deadlines[1])
	}
}
