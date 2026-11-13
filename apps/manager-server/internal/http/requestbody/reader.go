package requestbody

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

// DefaultReadIdleTimeout bounds how long a single Request.Body.Read may stall
// waiting for bytes before failing. It applies only while actively reading the
// request body, so handlers that have already finished reading their body (such
// as long-running Supervisor mutations) keep an undisturbed connection.
const DefaultReadIdleTimeout = 30 * time.Second

type readObserverKey struct{}

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

// Middleware wraps every incoming request body with Wrap(r.Context(), r.Body, w, idleTimeout).
func Middleware(idleTimeout time.Duration, next http.Handler) http.Handler {
	if next == nil {
		return http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r != nil && r.Body != nil && r.Body != http.NoBody {
			body, stopBody := Wrap(r.Context(), r.Body, w, idleTimeout)
			defer stopBody()
			r.Body = body
		}
		next.ServeHTTP(w, r)
	})
}

// Reader wraps an HTTP request body so in-flight reads unblock immediately when
// ctx cancels or Close is called, and each individual Read is bounded by
// idleTimeout via http.ResponseController.SetReadDeadline.
type Reader struct {
	ctx         context.Context
	inner       io.ReadCloser
	rc          *http.ResponseController
	idleTimeout time.Duration
	onRead      func(attempt int)

	mu        sync.Mutex
	reads     int
	reading   bool
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
	}
	obs, _ := ctx.Value(readObserverKey{}).(func(int))
	r := &Reader{
		ctx:         ctx,
		inner:       body,
		rc:          rc,
		idleTimeout: idleTimeout,
		onRead:      obs,
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
	if r.reading && r.rc != nil {
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
	canceled := r.canceled || r.closed || r.ctx.Err() != nil
	// Reset the read deadline between body chunks when more body remains. When
	// inner.Read hits io.EOF, net/http's startBackgroundRead has already cleared
	// the read deadline before starting its disconnect watcher.
	if r.rc != nil && !canceled && err == nil && r.idleTimeout > 0 {
		_ = r.rc.SetReadDeadline(time.Time{})
	}
	r.mu.Unlock()

	if err != nil && r.ctx.Err() != nil {
		return n, r.ctx.Err()
	}
	return n, err
}

func (r *Reader) Close() error {
	r.stop()
	r.mu.Lock()
	r.closed = true
	// Unblock any active net.Conn.Read before calling inner.Close(), because
	// net/http's (*body).Close acquires the same mutex held by (*body).Read.
	if r.reading && r.rc != nil {
		_ = r.rc.SetReadDeadline(time.Unix(1, 0))
	}
	r.mu.Unlock()
	return r.inner.Close()
}
