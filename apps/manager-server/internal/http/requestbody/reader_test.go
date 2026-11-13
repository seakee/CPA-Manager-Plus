package requestbody

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
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
