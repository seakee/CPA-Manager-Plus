package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/seakee/cpa-manager-plus/apps/ingress/internal/ingress"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	defaultAddr            = "0.0.0.0:18317"
	defaultManagerURL      = "http://cpamp-manager:18317"
	defaultGatewayURL      = "http://cpamp-runtime:8317"
	defaultShutdownTimeout = 10 * time.Second
)

type config struct {
	addr            string
	managerURL      *url.URL
	gatewayURL      *url.URL
	controlAddr     string
	shutdownTimeout time.Duration
}

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("configure CPAMP Ingress: %v", err)
	}
	logger := log.New(os.Stderr, "cpamp-ingress: ", log.LstdFlags)
	fresh := &ingress.FreshRoutes{}
	authority := managerFreshAuthority(cfg.managerURL)
	posture := managerPreFreshPosture(authority)
	handler := ingress.NewFreshHandler(fresh, cfg.managerURL, cfg.gatewayURL, logger, func(ctx context.Context) bool {
		return posture(ctx) == ingress.PreFreshLegacyGateway
	})
	handler.EnablePreFreshPosture(posture)
	// Fresh Gateway traffic opens only on a current, consistently bound private
	// Manager admission answer. Legacy 204 passthrough above is unchanged.
	handler.EnableFreshGatewayAdmission(ingress.NewGatewayAdmitter(fresh, authority, ingress.DefaultGatewayAdmissionTTL).Admitted)
	s2a, s2b := os.Getenv("CPAMP_PRIVATE_S2A_CHAT") == "1", os.Getenv("CPAMP_PRIVATE_S2B_CHAT") == "1"
	if s2a && s2b {
		logger.Fatal("conflicting private chat profiles")
	}
	if s2b && !handler.EnablePrivateS2BChat(cfg.managerURL, cfg.gatewayURL, os.Getenv("CPAMP_PRIVATE_S2B_MODEL")) {
		logger.Fatal("invalid private S2-B startup fixture")
	}
	if s2a {
		handler.EnablePrivateChat(cfg.managerURL, cfg.gatewayURL)
	}
	tracker := newDrainTracker(handler)
	control := &http.Server{Addr: cfg.controlAddr, Handler: fresh.ControlHandler(freshRoutingOnly(authority)), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 1 << 16}
	server := &http.Server{
		Addr:              cfg.addr,
		Handler:           tracker,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result := make(chan error, 2)
	go func() { result <- control.ListenAndServe() }()
	go func() {
		logger.Printf("listening on %s", cfg.addr)
		result <- server.ListenAndServe()
	}()

	select {
	case err := <-result:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("serve: %v", err)
		}
	case <-ctx.Done():
		if err := drainServers(context.Background(), cfg.shutdownTimeout, server, control, tracker); err != nil {
			logger.Fatalf("shutdown: %v", err)
		}
	}
}

func loadConfig(getenv func(string) string) (config, error) {
	return loadConfigWithBuild(getenv)
}

func loadConfigWithBuild(getenv func(string) string) (config, error) {
	addr := strings.TrimSpace(getenv("CPAMP_INGRESS_ADDR"))
	if addr == "" {
		addr = defaultAddr
	}
	managerURL, err := parseUpstream("CPAMP_MANAGER_URL", getenv("CPAMP_MANAGER_URL"), defaultManagerURL)
	if err != nil {
		return config{}, err
	}
	gatewayURL, err := parseUpstream("CPAMP_GATEWAY_URL", getenv("CPAMP_GATEWAY_URL"), defaultGatewayURL)
	if err != nil {
		return config{}, err
	}
	controlAddr := strings.TrimSpace(getenv("CPAMP_INGRESS_CONTROL_ADDR"))
	if controlAddr == "" {
		controlAddr = "0.0.0.0:9082"
	}
	shutdownTimeout, err := parseShutdownTimeout(getenv("CPAMP_INGRESS_SHUTDOWN_TIMEOUT"))
	if err != nil {
		return config{}, err
	}
	return config{
		addr:            addr,
		managerURL:      managerURL,
		gatewayURL:      gatewayURL,
		controlAddr:     controlAddr,
		shutdownTimeout: shutdownTimeout,
	}, nil
}

func parseShutdownTimeout(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultShutdownTimeout, nil
	}
	timeout, err := time.ParseDuration(raw)
	if err != nil || timeout <= 0 {
		return 0, errors.New("CPAMP_INGRESS_SHUTDOWN_TIMEOUT must be a positive duration")
	}
	return timeout, nil
}

func drainServers(parent context.Context, timeout time.Duration, server, control *http.Server, tracker *drainTracker) error {
	shutdownCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	err := server.Shutdown(shutdownCtx)
	if tracker != nil {
		if waitErr := tracker.Wait(shutdownCtx); err == nil {
			err = waitErr
		}
	}
	if control != nil {
		_ = control.Shutdown(shutdownCtx)
	}
	return err
}

type drainTracker struct {
	next     http.Handler
	wg       sync.WaitGroup
	mu       sync.Mutex
	hijacked map[net.Conn]struct{}
}

func newDrainTracker(next http.Handler) *drainTracker {
	return &drainTracker{
		next:     next,
		hijacked: make(map[net.Conn]struct{}),
	}
}

func (d *drainTracker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.wg.Add(1)
	defer d.wg.Done()
	d.next.ServeHTTP(&drainResponseWriter{ResponseWriter: w, tracker: d}, r)
}

func (d *drainTracker) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		d.closeHijacked()
		return ctx.Err()
	}
}

func (d *drainTracker) trackHijacked(conn net.Conn) net.Conn {
	d.wg.Add(1)
	wrapped := &trackedConn{Conn: conn, tracker: d}
	d.mu.Lock()
	d.hijacked[wrapped] = struct{}{}
	d.mu.Unlock()
	return wrapped
}

func (d *drainTracker) releaseHijacked(conn net.Conn) {
	d.mu.Lock()
	_, ok := d.hijacked[conn]
	if ok {
		delete(d.hijacked, conn)
	}
	d.mu.Unlock()
	if ok {
		d.wg.Done()
	}
}

func (d *drainTracker) closeHijacked() {
	d.mu.Lock()
	conns := make([]net.Conn, 0, len(d.hijacked))
	for conn := range d.hijacked {
		conns = append(conns, conn)
	}
	d.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

type drainResponseWriter struct {
	http.ResponseWriter
	tracker *drainTracker
}

func (w *drainResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *drainResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijack not supported")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, err
	}
	return w.tracker.trackHijacked(conn), rw, nil
}

func (w *drainResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

type trackedConn struct {
	net.Conn
	tracker   *drainTracker
	closeOnce sync.Once
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.closeOnce.Do(func() {
		c.tracker.releaseHijacked(c)
	})
	return err
}

var (
	errLegacyManager      = errors.New("Manager is serving the legacy application")
	errPreCommitAuthority = errors.New("Manager has no Fresh routing checkpoint yet")
)

// Only the fully started legacy Manager serves 204 on the existing private
// authority endpoint. Unknown/Fresh/startup/error responses never open Gateway.
// Retain this process-local mode observation for legacy Manager-outage continuity;
// an Ingress restart must obtain it again. Fresh admission cannot replace an
// initialized legacy store through normal startup.
func managerLegacyGateway(authority func(context.Context) (ingress.FreshAuthority, error)) func(context.Context) bool {
	posture := managerPreFreshPosture(authority)
	return func(ctx context.Context) bool {
		return posture(ctx) == ingress.PreFreshLegacyGateway
	}
}

func managerPreFreshPosture(authority func(context.Context) (ingress.FreshAuthority, error)) func(context.Context) ingress.PreFreshPosture {
	var confirmed atomic.Bool
	return func(ctx context.Context) ingress.PreFreshPosture {
		if confirmed.Load() {
			return ingress.PreFreshLegacyGateway
		}
		reply, err := authority(ctx)
		switch {
		case errors.Is(err, errLegacyManager):
			confirmed.Store(true)
			return ingress.PreFreshLegacyGateway
		case errors.Is(err, errPreCommitAuthority):
			return ingress.PreFreshBootstrap
		case err == nil && reply.FreshRouteState.Valid() && !reply.Committed:
			return ingress.PreFreshBootstrap
		default:
			return ingress.PreFreshPendingClosed
		}
	}
}

// freshRoutingOnly narrows the private authority client to the durable routing
// projection. Gateway admission is a read-only posture and never enters a
// routing write or the control listener.
func freshRoutingOnly(authority func(context.Context) (ingress.FreshAuthority, error)) func(context.Context) (ingress.FreshRouteState, error) {
	return func(ctx context.Context) (ingress.FreshRouteState, error) {
		reply, err := authority(ctx)
		return reply.FreshRouteState, err
	}
}

func managerFreshAuthority(managerURL *url.URL) func(context.Context) (ingress.FreshAuthority, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{
		Transport: transport,
		Timeout:   5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	endpoint := *managerURL
	endpoint.Path = ingress.FreshRoutingAuthorityPath
	endpoint.RawPath = ""
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	return func(ctx context.Context) (ingress.FreshAuthority, error) {
		var reply ingress.FreshAuthority
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return reply, err
		}
		req.Header.Set("Cache-Control", "no-store")
		res, err := client.Do(req)
		if err != nil {
			return reply, err
		}
		defer res.Body.Close()
		if res.StatusCode == http.StatusNoContent {
			return reply, errLegacyManager
		}
		if res.StatusCode == http.StatusNotFound {
			if decodesPreCommitBootstrapSignal(res) {
				return reply, errPreCommitAuthority
			}
			return reply, errors.New("Manager routing authority not found")
		}
		if res.StatusCode != http.StatusOK {
			return reply, fmt.Errorf("Manager routing authority unavailable: status %d", res.StatusCode)
		}
		decoder := json.NewDecoder(io.LimitReader(res.Body, 8192))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&reply); err != nil {
			return ingress.FreshAuthority{}, err
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			return ingress.FreshAuthority{}, errors.New("Manager routing authority returned trailing data")
		}
		return reply, nil
	}
}

const maxPreCommitBootstrapSignalBytes = 256

func decodesPreCommitBootstrapSignal(res *http.Response) bool {
	mediaType, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return false
	}
	var buf [maxPreCommitBootstrapSignalBytes + 1]byte
	n, err := io.ReadFull(res.Body, buf[:])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false
	}
	if n > maxPreCommitBootstrapSignalBytes {
		return false
	}
	var signal struct {
		BootstrapUI bool `json:"bootstrapUI"`
	}
	decoder := json.NewDecoder(bytes.NewReader(buf[:n]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&signal); err != nil || !signal.BootstrapUI {
		return false
	}
	var trailing any
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}

func parseUpstream(name, value, fallback string) (*url.URL, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("%s must be an http origin without credentials, path, query, or fragment", name)
	}
	parsed.Path = ""
	return parsed, nil
}
