package requestbody

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
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
				body, stopBody := Wrap(reqCtx, r.Body, w, 10*time.Second)
				defer stopBody()
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

	t.Run("idle_timeout_bounds_stalled_body_without_context_cancel", func(t *testing.T) {
		readErrCh := make(chan error, 1)
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, stopBody := Wrap(r.Context(), r.Body, w, 40*time.Millisecond)
				defer stopBody()
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
