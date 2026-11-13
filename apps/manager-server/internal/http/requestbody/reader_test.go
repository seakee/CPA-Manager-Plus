package requestbody

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
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
