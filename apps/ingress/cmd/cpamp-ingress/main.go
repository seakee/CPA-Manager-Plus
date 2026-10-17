package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/ingress/internal/ingress"
)

const (
	defaultAddr       = "0.0.0.0:18317"
	defaultManagerURL = "http://cpamp-manager:18317"
	defaultGatewayURL = "http://cpamp-runtime:8317"
)

type config struct {
	addr        string
	managerURL  *url.URL
	gatewayURL  *url.URL
	controlAddr string
}

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("configure CPAMP Ingress: %v", err)
	}
	logger := log.New(os.Stderr, "cpamp-ingress: ", log.LstdFlags)
	fresh := &ingress.FreshRoutes{}
	authority := managerFreshAuthority(cfg.managerURL)
	handler := ingress.NewFreshHandler(fresh, cfg.managerURL, cfg.gatewayURL, logger, managerLegacyGateway(authority))
	control := &http.Server{Addr: cfg.controlAddr, Handler: fresh.ControlHandler(authority), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 1 << 16}
	server := &http.Server{
		Addr:              cfg.addr,
		Handler:           handler,
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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Fatalf("shutdown: %v", err)
		}
		_ = control.Shutdown(shutdownCtx)
	}
}

func loadConfig(getenv func(string) string) (config, error) {
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
	return config{addr: addr, managerURL: managerURL, gatewayURL: gatewayURL, controlAddr: controlAddr}, nil
}

var errLegacyManager = errors.New("Manager is serving the legacy application")

// Only the fully started legacy Manager serves 204 on the existing private
// authority endpoint. Unknown/Fresh/startup/error responses never open Gateway.
// Retain this process-local mode observation for legacy Manager-outage continuity;
// an Ingress restart must obtain it again. Fresh admission cannot replace an
// initialized legacy store through normal startup.
func managerLegacyGateway(authority func(context.Context) (ingress.FreshRouteState, error)) func(context.Context) bool {
	var confirmed atomic.Bool
	return func(ctx context.Context) bool {
		if !confirmed.Load() {
			_, err := authority(ctx)
			if errors.Is(err, errLegacyManager) {
				confirmed.Store(true)
			}
		}
		return confirmed.Load()
	}
}

func managerFreshAuthority(managerURL *url.URL) func(context.Context) (ingress.FreshRouteState, error) {
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
	return func(ctx context.Context) (ingress.FreshRouteState, error) {
		var state ingress.FreshRouteState
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return state, err
		}
		req.Header.Set("Cache-Control", "no-store")
		res, err := client.Do(req)
		if err != nil {
			return state, err
		}
		defer res.Body.Close()
		if res.StatusCode == http.StatusNoContent {
			return state, errLegacyManager
		}
		if res.StatusCode != http.StatusOK {
			return state, fmt.Errorf("Manager routing authority unavailable: status %d", res.StatusCode)
		}
		decoder := json.NewDecoder(io.LimitReader(res.Body, 8192))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&state); err != nil {
			return ingress.FreshRouteState{}, err
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			return ingress.FreshRouteState{}, errors.New("Manager routing authority returned trailing data")
		}
		return state, nil
	}
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
