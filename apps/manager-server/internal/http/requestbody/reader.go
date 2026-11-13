package requestbody

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

// DefaultReadIdleTimeout is 0 (disabled) in normal operation so slow or
// multi-chunk streaming uploads preserve the server's pre-existing unlimited
// inter-chunk read behavior, while context cancellation on shutdown still
// aborts active reads immediately via ResponseController.SetReadDeadline.
const DefaultReadIdleTimeout time.Duration = 0

// DefaultEarlyCloseDrainTimeout bounds net/http's opportunistic post-handler
// HTTP/1.1 keep-alive body drain when a request body is closed before reaching
// io.EOF (for example, an over-limit body rejection or auth failure). Normal
// requests that read their body to io.EOF never enter this drain path.
const DefaultEarlyCloseDrainTimeout = 250 * time.Millisecond

type readObserverKey struct{}
type closeObserverKey struct{}
type earlyCloseDrainTimeoutKey struct{}

// WithReadObserver attaches an observer invoked right before each underlying
// socket Read attempt (1-based attempt index). Intended for deterministic tests.
func WithReadObserver(ctx context.Context, fn func(attempt int)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, readObserverKey{}, fn)
}

// WithNonEOFCloseObserver attaches an observer invoked right before inner.Close
// runs on a body that has not yet reached io.EOF. Intended for deterministic tests.
func WithNonEOFCloseObserver(ctx context.Context, fn func()) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, closeObserverKey{}, fn)
}

// WithEarlyCloseDrainTimeout overrides DefaultEarlyCloseDrainTimeout on bodies
// wrapped with ctx. A negative value disables the normal-operation drain cap so
// tests can isolate pure context-cancellation interruption during Close().
func WithEarlyCloseDrainTimeout(ctx context.Context, d time.Duration) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, earlyCloseDrainTimeoutKey{}, d)
}

// Middleware wraps incoming non-empty request bodies with Wrap and ensures
// body.Close() runs before ServeHTTP returns so net/http's finishRequest never
// performs an unguarded post-handler body drain.
func Middleware(idleTimeout time.Duration, next http.Handler) http.Handler {
	if next == nil {
		return http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r != nil && r.Body != nil && r.Body != http.NoBody {
			body, stopBody := Wrap(r.Context(), r.Body, w, idleTimeout)
			defer stopBody()
			defer func() { _ = body.Close() }()
			r.Body = body
		}
		next.ServeHTTP(w, r)
	})
}

// Reader wraps an HTTP request body so in-flight reads and non-EOF early-close
// drains unblock immediately when ctx cancels, while preserving normal slow
// streaming reads and HTTP/1.1 keep-alive connection reuse on EOF.
type Reader struct {
	ctx                    context.Context
	inner                  io.ReadCloser
	rc                     *http.ResponseController
	idleTimeout            time.Duration
	earlyCloseDrainTimeout time.Duration
	onRead                 func(attempt int)
	onBeforeNonEOFClose    func()

	mu        sync.Mutex
	reads     int
	reading   bool
	closing   bool
	eof       bool
	canceled  bool
	closed    bool
	stopWatch func() bool
}

// Wrap returns a Reader-wrapped body and a cleanup function that unregisters
// its context watcher. If body is already a *Reader, Wrap reuses it (tightening
// idleTimeout when a smaller positive timeout is requested).
func Wrap(
	ctx context.Context,
	body io.ReadCloser,
	w http.ResponseWriter,
	idleTimeout time.Duration,
) (io.ReadCloser, func()) {
	if body == nil || body == http.NoBody {
		return body, func() {}
	}
	if existing, ok := body.(*Reader); ok {
		if idleTimeout > 0 {
			existing.mu.Lock()
			if existing.idleTimeout <= 0 || idleTimeout < existing.idleTimeout {
				existing.idleTimeout = idleTimeout
			}
			existing.mu.Unlock()
		}
		return existing, func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var rc *http.ResponseController
	if w != nil {
		rc = http.NewResponseController(w)
		// Prevent net/http's chunkWriter.writeHeader from auto-closing raw
		// w.reqBody ahead of Reader.Close() when a handler flushes >2048 bytes
		// without consuming r.Body (e.g. GET /favicon.ico with Content-Length > 0).
		_ = rc.EnableFullDuplex()
	}
	obs, _ := ctx.Value(readObserverKey{}).(func(int))
	closeObs, _ := ctx.Value(closeObserverKey{}).(func())
	drainTimeout := DefaultEarlyCloseDrainTimeout
	if custom, ok := ctx.Value(earlyCloseDrainTimeoutKey{}).(time.Duration); ok {
		if custom < 0 {
			drainTimeout = 0
		} else {
			drainTimeout = custom
		}
	}
	r := &Reader{
		ctx:                    ctx,
		inner:                  body,
		rc:                     rc,
		idleTimeout:            idleTimeout,
		earlyCloseDrainTimeout: drainTimeout,
		onRead:                 obs,
		onBeforeNonEOFClose:    closeObs,
	}
	r.stopWatch = context.AfterFunc(ctx, r.abortActiveRead)
	return r, r.stop
}

func (r *Reader) stop() {
	if r == nil || r.stopWatch == nil {
		return
	}
	r.stopWatch()
}

func (r *Reader) abortActiveRead() {
	r.mu.Lock()
	r.canceled = true
	if !r.eof && (!r.closed || r.closing) && r.rc != nil {
		_ = r.rc.SetReadDeadline(time.Unix(1, 0))
	}
	r.mu.Unlock()
}

func (r *Reader) Read(p []byte) (int, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return 0, http.ErrBodyReadAfterClose
	}
	if r.eof {
		r.mu.Unlock()
		return 0, io.EOF
	}
	if r.canceled || r.ctx.Err() != nil {
		r.canceled = true
		r.mu.Unlock()
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		return 0, context.Canceled
	}
	r.reads++
	attempt := r.reads
	r.reading = true
	if r.rc != nil && r.idleTimeout > 0 {
		_ = r.rc.SetReadDeadline(time.Now().Add(r.idleTimeout))
	}
	r.mu.Unlock()

	if r.onRead != nil {
		r.onRead(attempt)
	}
	n, err := r.inner.Read(p)

	r.mu.Lock()
	r.reading = false
	sawEOF := errors.Is(err, io.EOF)
	if sawEOF {
		r.eof = true
	}
	canceled := r.canceled || r.closed || r.ctx.Err() != nil
	// Clear the read deadline both between body chunks (err == nil) and on any
	// io.EOF return so a post-EOF read deadline never arms net/http's
	// backgroundRead disconnect watcher.
	if r.rc != nil && !canceled && r.idleTimeout > 0 && (err == nil || sawEOF) {
		_ = r.rc.SetReadDeadline(time.Time{})
	}
	r.mu.Unlock()

	if err != nil && r.ctx.Err() != nil {
		return n, r.ctx.Err()
	}
	return n, err
}

func (r *Reader) Close() error {
	if r == nil {
		return nil
	}
	// Keep stopWatch armed until inner.Close() finishes so context cancellation
	// mid-Close immediately aborts net/http's early-close body drain.
	defer r.stop()

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	eof := r.eof
	alreadyCanceled := r.canceled || r.ctx.Err() != nil
	if !eof {
		r.closing = true
		if r.rc != nil {
			switch {
			case alreadyCanceled:
				_ = r.rc.SetReadDeadline(time.Unix(1, 0))
			case r.earlyCloseDrainTimeout > 0:
				_ = r.rc.SetReadDeadline(time.Now().Add(r.earlyCloseDrainTimeout))
			}
		}
	}
	onClose := r.onBeforeNonEOFClose
	r.mu.Unlock()

	if !eof && onClose != nil {
		onClose()
	}
	err := r.inner.Close()

	r.mu.Lock()
	r.closing = false
	canceled := r.canceled || r.ctx.Err() != nil
	if !eof && err == nil && !canceled && r.rc != nil && r.earlyCloseDrainTimeout > 0 {
		// Remaining unread bytes arrived within the drain window and reached EOF;
		// clear the temporary drain deadline so HTTP/1.1 keep-alive can reuse the conn.
		_ = r.rc.SetReadDeadline(time.Time{})
	}
	r.mu.Unlock()
	return err
}
