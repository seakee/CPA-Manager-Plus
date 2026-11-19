package requestbody

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
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

// maxPostHandlerReadBytes matches Go net/http's internal post-handler body
// drain threshold (256 KiB).
const maxPostHandlerReadBytes = 256 << 10

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
		var tracker *headerCommitWriter
		if w != nil {
			w, tracker = wrapHeaderCommitWriter(w)
			defer func() {
				if tracker != nil {
					if wrote, _ := tracker.state(); !wrote && headerHasConnectionCloseToken(w.Header()) {
						normalizeConnectionCloseHeader(w.Header())
					}
				}
			}()
		}
		if r != nil && r.Body != nil && r.Body != http.NoBody {
			origBody := r.Body
			body, stopBody := wrapRequestBody(r.Context(), origBody, w, idleTimeout, r.ContentLength)
			defer func() {
				_ = body.Close()
				r.Body = origBody
				stopBody()
			}()
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
	rw                     http.ResponseWriter
	tracker                *headerCommitWriter
	rc                     *http.ResponseController
	idleTimeout            time.Duration
	earlyCloseDrainTimeout time.Duration
	contentLength          int64
	onRead                 func(attempt int)
	onBeforeNonEOFClose    func()

	mu        sync.Mutex
	reads     int
	bytesRead int64
	reading   bool
	closing   bool
	eof       bool
	canceled  bool
	closed    bool
	closeDone chan struct{}
	closeErr  error
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
	return wrapRequestBody(ctx, body, w, idleTimeout, -1)
}

func wrapRequestBody(
	ctx context.Context,
	body io.ReadCloser,
	w http.ResponseWriter,
	idleTimeout time.Duration,
	contentLength int64,
) (io.ReadCloser, func()) {
	if body == nil || body == http.NoBody {
		return body, func() {}
	}
	if existing, ok := body.(*Reader); ok {
		existing.mu.Lock()
		if existing.rw == nil && w != nil {
			existing.rw = w
		}
		if existing.tracker == nil && w != nil {
			existing.tracker = findHeaderCommitWriter(w)
		}
		if existing.contentLength < 0 && contentLength >= 0 {
			existing.contentLength = contentLength
		}
		if idleTimeout > 0 && (existing.idleTimeout <= 0 || idleTimeout < existing.idleTimeout) {
			existing.idleTimeout = idleTimeout
		}
		existing.mu.Unlock()
		return existing, func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var rc *http.ResponseController
	var tracker *headerCommitWriter
	if w != nil {
		tracker = findHeaderCommitWriter(w)
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
		rw:                     w,
		tracker:                tracker,
		rc:                     rc,
		idleTimeout:            idleTimeout,
		earlyCloseDrainTimeout: drainTimeout,
		contentLength:          contentLength,
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

func (r *Reader) responseClosing() (closing bool, exactBefore bool) {
	if r == nil || r.rw == nil {
		return false, false
	}
	h := r.rw.Header()
	if r.tracker != nil {
		wroteHeader, committedClose := r.tracker.state()
		if wroteHeader {
			return committedClose, committedClose
		}
		if headerHasConnectionCloseToken(h) {
			normalizeConnectionCloseHeader(h)
			return true, true
		}
		return false, false
	}
	if headerHasConnectionCloseToken(h) {
		exact := headerHasExactConnectionClose(h) && len(h.Values("Connection")) == 1
		normalizeConnectionCloseHeader(h)
		return true, exact
	}
	return false, false
}

func headerHasExactConnectionClose(h http.Header) bool {
	return h.Get("Connection") == "close"
}

func normalizeConnectionCloseHeader(h http.Header) {
	if h.Get("Connection") != "close" || len(h.Values("Connection")) != 1 {
		h.Set("Connection", "close")
	}
}

func headerHasConnectionCloseToken(h http.Header) bool {
	for _, v := range h.Values("Connection") {
		for len(v) > 0 {
			part := v
			if idx := strings.IndexByte(v, ','); idx >= 0 {
				part = v[:idx]
				v = v[idx+1:]
			} else {
				v = ""
			}
			if strings.EqualFold(strings.TrimSpace(part), "close") {
				return true
			}
		}
	}
	return false
}

type headerCommitWriter struct {
	http.ResponseWriter
	mu                 sync.Mutex
	wroteHeader        bool
	committedConnClose bool
}

func wrapHeaderCommitWriter(w http.ResponseWriter) (http.ResponseWriter, *headerCommitWriter) {
	if w == nil {
		return nil, nil
	}
	if existing := findHeaderCommitWriter(w); existing != nil {
		return w, existing
	}
	tracker := &headerCommitWriter{ResponseWriter: w}
	return tracker, tracker
}

func findHeaderCommitWriter(w http.ResponseWriter) *headerCommitWriter {
	type unwrapper interface {
		Unwrap() http.ResponseWriter
	}
	for w != nil {
		if tracker, ok := w.(*headerCommitWriter); ok {
			return tracker
		}
		u, ok := w.(unwrapper)
		if !ok {
			return nil
		}
		next := u.Unwrap()
		if next == nil || next == w {
			return nil
		}
		w = next
	}
	return nil
}

func supportsFlush(w http.ResponseWriter) bool {
	type unwrapper interface {
		Unwrap() http.ResponseWriter
	}
	for w != nil {
		switch w.(type) {
		case interface{ FlushError() error }, http.Flusher:
			return true
		}
		u, ok := w.(unwrapper)
		if !ok {
			return false
		}
		next := u.Unwrap()
		if next == nil || next == w {
			return false
		}
		w = next
	}
	return false
}

func (w *headerCommitWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *headerCommitWriter) commitHeader(statusCode int) {
	if statusCode >= 100 && statusCode <= 199 && statusCode != http.StatusSwitchingProtocols {
		return
	}
	w.mu.Lock()
	if !w.wroteHeader {
		w.wroteHeader = true
		h := w.ResponseWriter.Header()
		if statusCode != http.StatusSwitchingProtocols && headerHasConnectionCloseToken(h) {
			normalizeConnectionCloseHeader(h)
			w.committedConnClose = true
		} else {
			w.committedConnClose = statusCode != http.StatusSwitchingProtocols && headerHasExactConnectionClose(h)
		}
	}
	w.mu.Unlock()
}

func (w *headerCommitWriter) WriteHeader(statusCode int) {
	w.commitHeader(statusCode)
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *headerCommitWriter) Write(p []byte) (int, error) {
	w.commitHeader(http.StatusOK)
	return w.ResponseWriter.Write(p)
}

func (w *headerCommitWriter) FlushError() error {
	if !supportsFlush(w.ResponseWriter) {
		return http.NewResponseController(w.ResponseWriter).Flush()
	}
	w.commitHeader(http.StatusOK)
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *headerCommitWriter) Flush() {
	_ = w.FlushError()
}

func (w *headerCommitWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.mu.Lock()
		w.wroteHeader = true
		w.committedConnClose = false
		w.mu.Unlock()
	}
	return conn, rw, err
}

func (w *headerCommitWriter) state() (wroteHeader bool, committedConnClose bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.wroteHeader, w.committedConnClose
}

type singleByteReadCloser struct{}

func (singleByteReadCloser) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = 0
	return 1, io.EOF
}

func (singleByteReadCloser) Close() error { return nil }

// UnwrapRootResponseWriter walks any Unwrap() http.ResponseWriter chain down
// to the underlying server ResponseWriter so net/http internal type assertions
// (such as http.MaxBytesReader's requestTooLarge hook) reach the root writer.
func UnwrapRootResponseWriter(w http.ResponseWriter) http.ResponseWriter {
	type unwrapper interface {
		Unwrap() http.ResponseWriter
	}
	for w != nil {
		u, ok := w.(unwrapper)
		if !ok {
			return w
		}
		next := u.Unwrap()
		if next == nil || next == w {
			return w
		}
		w = next
	}
	return nil
}

// markResponseCloseAfterReply invokes Go net/http's internal requestTooLarge()
// hook on the root server ResponseWriter so net/http sets closeAfterReply=true
// even when a <=256 KiB drain times out or exits early after WriteHeader.
func markResponseCloseAfterReply(w http.ResponseWriter) {
	rootW := UnwrapRootResponseWriter(w)
	if rootW == nil {
		return
	}
	var one [1]byte
	_, _ = http.MaxBytesReader(rootW, singleByteReadCloser{}, 0).Read(one[:])
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
	if n > 0 {
		r.bytesRead += int64(n)
	}
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

	if err != nil && !sawEOF && r.ctx.Err() != nil {
		return n, r.ctx.Err()
	}
	return n, err
}

func (r *Reader) Close() error {
	if r == nil {
		return nil
	}
	closingConn, exactConnCloseBefore := r.responseClosing()

	r.mu.Lock()
	if r.closed {
		done := r.closeDone
		r.mu.Unlock()
		<-done
		r.mu.Lock()
		err := r.closeErr
		r.mu.Unlock()
		return err
	}
	r.closed = true
	done := make(chan struct{})
	r.closeDone = done
	eof := r.eof
	alreadyCanceled := r.canceled || r.ctx.Err() != nil
	tooMuchRemaining := r.contentLength > maxPostHandlerReadBytes &&
		r.contentLength-r.bytesRead > maxPostHandlerReadBytes
	skipDrain := alreadyCanceled || closingConn || tooMuchRemaining
	if !eof {
		r.closing = true
		if r.rc != nil {
			switch {
			case skipDrain:
				_ = r.rc.SetReadDeadline(time.Unix(1, 0))
			case r.earlyCloseDrainTimeout > 0:
				_ = r.rc.SetReadDeadline(time.Now().Add(r.earlyCloseDrainTimeout))
			}
		}
	}
	onClose := r.onBeforeNonEOFClose
	r.mu.Unlock()

	var err error
	unknownLenExceededDrain := false
	defer func() {
		// Unregister stopWatch only after inner.Close() completes so concurrent
		// Close() callers never disarm the active cancellation watcher early.
		r.stop()
		afterClosing, _ := r.responseClosing()
		connClosedAfter := closingConn || afterClosing
		r.mu.Lock()
		r.closing = false
		r.closeErr = err
		canceled := r.canceled || r.ctx.Err() != nil || connClosedAfter
		cleanDrain := !skipDrain && !unknownLenExceededDrain && err == nil && !canceled
		if !eof && cleanDrain {
			r.eof = true
		}
		rw := r.rw
		rc := r.rc
		drainTimeout := r.earlyCloseDrainTimeout
		tracker := r.tracker
		r.mu.Unlock()

		if !eof {
			if cleanDrain {
				if rc != nil && drainTimeout > 0 {
					// Remaining unread bytes arrived within the drain window and reached EOF;
					// clear the temporary drain deadline so HTTP/1.1 keep-alive can reuse the conn.
					_ = rc.SetReadDeadline(time.Time{})
				}
			} else {
				markResponseCloseAfterReply(rw)
			}
		} else if closingConn && tracker == nil && !exactConnCloseBefore {
			markResponseCloseAfterReply(rw)
		}
		close(done)
	}()

	if !eof && onClose != nil {
		onClose()
	}
	if !eof && !skipDrain && r.contentLength < 0 {
		// Unknown/chunked body length: bound our own pre-close drain to
		// maxPostHandlerReadBytes so >256 KiB chunked unread bodies force
		// closeAfterReply even if callers used Wrap without restoring origBody.
		_, drainErr := io.CopyN(io.Discard, r.inner, maxPostHandlerReadBytes+1)
		switch {
		case errors.Is(drainErr, io.EOF):
			r.mu.Lock()
			r.eof = true
			r.mu.Unlock()
		case drainErr == nil:
			unknownLenExceededDrain = true
			if r.rc != nil {
				_ = r.rc.SetReadDeadline(time.Unix(1, 0))
			}
		default:
			if closeErr := r.inner.Close(); closeErr != nil {
				err = closeErr
			} else {
				err = drainErr
			}
			return err
		}
	}
	err = r.inner.Close()
	return err
}
