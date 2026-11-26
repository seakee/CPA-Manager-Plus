package middleware

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	// TrialReleasePath is the internal loopback endpoint invoked by cpamp-launcher
	// to release the write hold immediately upon committing a 60s slot trial.
	TrialReleasePath = "/internal/v1/trial/release"

	// TrialHoldErrorCode is returned in 503 responses while post-upgrade trial
	// verification is holding external write operations.
	TrialHoldErrorCode = "upgrade_trial_verifying"

	// TrialHoldErrorMessage explains why writes are temporarily held during trial.
	TrialHoldErrorMessage = "CPAMP is completing post-upgrade health verification; writes resume automatically within 60s"

	trialHoldRetryAfterSeconds = "5"
	maxLauncherTokenBytes      = 4096
)

var (
	currentProcessTrialHoldGate    atomic.Pointer[TrialHoldGate]
	fallbackProcessTrialHoldActive atomic.Bool
	fallbackTrialHoldMu            sync.Mutex
	fallbackTrialHoldActivatedCh   chan struct{}
	closedTrialHoldChan            = func() chan struct{} {
		ch := make(chan struct{})
		close(ch)
		return ch
	}()
)

type trialHoldTrackedMutationKey struct {
	gate *TrialHoldGate
}

// SetProcessTrialHoldGate registers (or clears when nil) the process-wide
// TrialHoldGate so health reporting reflects live in-memory release transitions.
func SetProcessTrialHoldGate(gate *TrialHoldGate) {
	fallbackTrialHoldMu.Lock()
	fallbackProcessTrialHoldActive.Store(false)
	fallbackTrialHoldActivatedCh = make(chan struct{})
	fallbackTrialHoldMu.Unlock()
	currentProcessTrialHoldGate.Store(gate)
}

// EngageProcessTrialHold activates the process-wide write hold so new external
// HTTP mutations and background queue/database mutators pause immediately.
func EngageProcessTrialHold() {
	fallbackTrialHoldMu.Lock()
	fallbackProcessTrialHoldActive.Store(true)
	if fallbackTrialHoldActivatedCh != nil && fallbackTrialHoldActivatedCh != closedTrialHoldChan {
		close(fallbackTrialHoldActivatedCh)
	}
	fallbackTrialHoldActivatedCh = closedTrialHoldChan
	fallbackTrialHoldMu.Unlock()
	if gate := currentProcessTrialHoldGate.Load(); gate != nil {
		gate.Engage()
	}
}

// ReleaseProcessTrialHold clears the process-wide write hold.
func ReleaseProcessTrialHold() {
	fallbackTrialHoldMu.Lock()
	fallbackProcessTrialHoldActive.Store(false)
	fallbackTrialHoldActivatedCh = make(chan struct{})
	fallbackTrialHoldMu.Unlock()
	if gate := currentProcessTrialHoldGate.Load(); gate != nil {
		gate.Release()
	}
}

// TrialHoldActivatedChan returns a channel that is already closed when the
// process-wide write hold is active, or closes atomically when Engage activates.
func TrialHoldActivatedChan() <-chan struct{} {
	if gate := currentProcessTrialHoldGate.Load(); gate != nil {
		return gate.ActivatedChan()
	}
	if IsProcessTrialHoldActive() {
		return closedTrialHoldChan
	}
	fallbackTrialHoldMu.Lock()
	if fallbackProcessTrialHoldActive.Load() {
		fallbackTrialHoldMu.Unlock()
		return closedTrialHoldChan
	}
	if fallbackTrialHoldActivatedCh == nil || fallbackTrialHoldActivatedCh == closedTrialHoldChan {
		fallbackTrialHoldActivatedCh = make(chan struct{})
	}
	ch := fallbackTrialHoldActivatedCh
	fallbackTrialHoldMu.Unlock()
	return ch
}

// WaitOtherInflightMutations blocks until all other in-flight non-read HTTP
// requests and background worker mutations passing through the process
// TrialHoldGate have completed or ctx ends.
func WaitOtherInflightMutations(ctx context.Context) error {
	if gate := currentProcessTrialHoldGate.Load(); gate != nil {
		return gate.WaitOtherInflightMutations(ctx)
	}
	if ctx != nil {
		return ctx.Err()
	}
	return nil
}

// BeginBackgroundMutation atomically checks the process-wide TrialHoldGate and
// registers an in-flight background worker mutation when inactive, returning an
// idempotent done callback that WaitOtherInflightMutations waits on.
func BeginBackgroundMutation() (done func(), blocked bool) {
	if gate := currentProcessTrialHoldGate.Load(); gate != nil {
		return gate.BeginBackgroundMutation()
	}
	if IsProcessTrialHoldActive() {
		return nil, true
	}
	return func() {}, false
}

// IsProcessTrialHoldActive reports whether the current process is holding
// external writes for pre-switch snapshot fencing or post-upgrade trial verification.
func IsProcessTrialHoldActive() bool {
	if gate := currentProcessTrialHoldGate.Load(); gate != nil {
		return gate.Active()
	}
	return fallbackProcessTrialHoldActive.Load() || strings.TrimSpace(os.Getenv("CPAMP_TRIAL_HOLD")) == "1"
}

// TrialHoldGate holds non-idempotent HTTP mutations during pre-switch snapshot
// fencing and the 60s post-upgrade slot trial window, and exposes a loopback-only
// Bearer-authenticated release endpoint.
type TrialHoldGate struct {
	mu          sync.Mutex
	cond        *sync.Cond
	active      atomic.Bool
	inflight    int
	tokenFile   string
	activatedCh chan struct{}
}

// NewTrialHoldGate constructs a TrialHoldGate with explicit initial state and
// launcher token file path.
func NewTrialHoldGate(initialActive bool, tokenFile string) *TrialHoldGate {
	activatedCh := make(chan struct{})
	if initialActive {
		activatedCh = closedTrialHoldChan
	}
	g := &TrialHoldGate{
		tokenFile:   strings.TrimSpace(tokenFile),
		activatedCh: activatedCh,
	}
	g.cond = sync.NewCond(&g.mu)
	g.active.Store(initialActive)
	return g
}

// NewTrialHoldGateFromEnv constructs a TrialHoldGate from CPAMP_TRIAL_HOLD and
// CPAMP_LAUNCHER_TOKEN_FILE.
func NewTrialHoldGateFromEnv() *TrialHoldGate {
	return NewTrialHoldGate(
		strings.TrimSpace(os.Getenv("CPAMP_TRIAL_HOLD")) == "1",
		strings.TrimSpace(os.Getenv("CPAMP_LAUNCHER_TOKEN_FILE")),
	)
}

func (g *TrialHoldGate) ensureCondLocked() *sync.Cond {
	if g.cond == nil {
		g.cond = sync.NewCond(&g.mu)
	}
	return g.cond
}
func (g *TrialHoldGate) ensureActivatedChanLocked() chan struct{} {
	if g.activatedCh == nil {
		if g.active.Load() {
			g.activatedCh = closedTrialHoldChan
		} else {
			g.activatedCh = make(chan struct{})
		}
	}
	return g.activatedCh
}

// Active reports whether the write hold is currently active.
func (g *TrialHoldGate) Active() bool {
	return g != nil && g.active.Load()
}

// ActivatedChan returns a channel that is already closed when the write hold is
// active, or closes atomically under g.mu when Engage activates.
func (g *TrialHoldGate) ActivatedChan() <-chan struct{} {
	if g == nil {
		if IsProcessTrialHoldActive() {
			return closedTrialHoldChan
		}
		return nil
	}
	g.mu.Lock()
	ch := g.ensureActivatedChanLocked()
	g.mu.Unlock()
	return ch
}

// Engage activates the in-memory write hold, fenced against concurrent request admission.
func (g *TrialHoldGate) Engage() {
	if g == nil {
		return
	}
	g.mu.Lock()
	ch := g.ensureActivatedChanLocked()
	if !g.active.Swap(true) && ch != closedTrialHoldChan {
		close(ch)
	}
	g.activatedCh = closedTrialHoldChan
	g.mu.Unlock()
}

// Release clears the in-memory write hold.
func (g *TrialHoldGate) Release() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.active.Swap(false) || g.activatedCh == nil || g.activatedCh == closedTrialHoldChan {
		g.activatedCh = make(chan struct{})
	}
	g.mu.Unlock()
}

// BeginBackgroundMutation atomically admits a background worker mutation when
// the write hold is inactive and returns an idempotent done callback. When the
// hold is active, it returns (nil, true) without incrementing inflight.
func (g *TrialHoldGate) BeginBackgroundMutation() (done func(), blocked bool) {
	if g == nil {
		return func() {}, false
	}
	g.mu.Lock()
	if g.active.Load() {
		g.mu.Unlock()
		return nil, true
	}
	g.inflight++
	g.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.inflight--
			g.ensureCondLocked().Broadcast()
			g.mu.Unlock()
		})
	}, false
}

// WaitOtherInflightMutations waits until all in-flight non-read HTTP mutations
// and background worker mutations other than the calling request itself finish.
func (g *TrialHoldGate) WaitOtherInflightMutations(ctx context.Context) error {
	if g == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	selfCount := 0
	if tracked, _ := ctx.Value(trialHoldTrackedMutationKey{gate: g}).(bool); tracked {
		selfCount = 1
	}
	stop := context.AfterFunc(ctx, func() {
		g.mu.Lock()
		g.ensureCondLocked().Broadcast()
		g.mu.Unlock()
	})
	defer stop()

	g.mu.Lock()
	defer g.mu.Unlock()
	cond := g.ensureCondLocked()
	for g.inflight > selfCount {
		if err := ctx.Err(); err != nil {
			return err
		}
		cond.Wait()
	}
	return ctx.Err()
}

// Wrap returns an http.Handler that handles POST /internal/v1/trial/release and
// enforces the read-only write hold while active.
func (g *TrialHoldGate) Wrap(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	if g == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r != nil && r.URL != nil && isCanonicalTrialReleaseURL(r.URL) {
			g.handleTrialRelease(w, r)
			return
		}
		if r == nil || isTrialHoldReadMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		g.mu.Lock()
		if g.active.Load() {
			g.mu.Unlock()
			writeTrialHoldUnavailable(w)
			return
		}
		g.inflight++
		g.mu.Unlock()
		defer func() {
			g.mu.Lock()
			g.inflight--
			g.ensureCondLocked().Broadcast()
			g.mu.Unlock()
		}()
		ctx := context.WithValue(r.Context(), trialHoldTrackedMutationKey{gate: g}, true)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (g *TrialHoldGate) handleTrialRelease(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if g.tokenFile == "" {
		writeTrialJSON(w, http.StatusNotFound, map[string]string{
			"code":    "not_found",
			"message": "trial release endpoint is not configured",
		})
		return
	}
	if !isStrictLoopbackRemoteAddr(r.RemoteAddr) {
		writeTrialJSON(w, http.StatusForbidden, map[string]string{
			"code":    "forbidden",
			"message": "loopback client required",
		})
		return
	}
	if hasProxyForwardedHeader(r.Header) {
		writeTrialJSON(w, http.StatusForbidden, map[string]string{
			"code":    "forbidden",
			"message": "proxied requests are not permitted",
		})
		return
	}
	if r.Method != http.MethodPost {
		writeTrialJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"code":    "method_not_allowed",
			"message": "method not allowed",
		})
		return
	}
	expectedToken, ok := readLauncherTokenFile(g.tokenFile)
	if !ok {
		writeTrialJSON(w, http.StatusUnauthorized, map[string]string{
			"code":    "unauthorized",
			"message": "invalid launcher token",
		})
		return
	}
	expectedBearerHash := sha256.Sum256([]byte("Bearer " + expectedToken))
	gotAuth := strings.TrimSpace(r.Header.Get("Authorization"))
	gotBearerHash := sha256.Sum256([]byte(gotAuth))
	if gotAuth == "" || subtle.ConstantTimeCompare(expectedBearerHash[:], gotBearerHash[:]) != 1 {
		writeTrialJSON(w, http.StatusUnauthorized, map[string]string{
			"code":    "unauthorized",
			"message": "invalid launcher token",
		})
		return
	}
	g.Release()
	writeTrialJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func hasProxyForwardedHeader(h http.Header) bool {
	for _, name := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded", "X-Real-IP"} {
		if len(h.Values(name)) > 0 {
			return true
		}
	}
	for key := range h {
		lower := strings.ToLower(strings.TrimSpace(key))
		if lower == "forwarded" || lower == "x-real-ip" || strings.HasPrefix(lower, "x-forwarded-") {
			return true
		}
	}
	return false
}

func isCanonicalTrialReleaseURL(u *url.URL) bool {
	if u == nil || u.Path != TrialReleasePath {
		return false
	}
	escaped := u.EscapedPath()
	return escaped == "" || escaped == TrialReleasePath
}

func isTrialHoldReadMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func isStrictLoopbackRemoteAddr(remoteAddr string) bool {
	trimmed := strings.TrimSpace(remoteAddr)
	if trimmed == "" {
		return false
	}
	host := trimmed
	if splitHost, _, err := net.SplitHostPort(trimmed); err == nil {
		host = splitHost
	} else {
		host = strings.TrimPrefix(strings.TrimSuffix(trimmed, "]"), "[")
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	unmapped := addr.Unmap()
	return unmapped == netip.MustParseAddr("127.0.0.1") || addr == netip.IPv6Loopback()
}

func readLauncherTokenFile(path string) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", false
	}
	if info.Size() <= 0 || info.Size() > maxLauncherTokenBytes {
		return "", false
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = file.Close() }()

	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return "", false
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxLauncherTokenBytes+1))
	if err != nil || len(raw) > maxLauncherTokenBytes {
		return "", false
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", false
	}
	return token, true
}

func writeTrialHoldUnavailable(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", trialHoldRetryAfterSeconds)
	writeTrialJSON(w, http.StatusServiceUnavailable, map[string]string{
		"code":    TrialHoldErrorCode,
		"message": TrialHoldErrorMessage,
	})
}

func writeTrialJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
