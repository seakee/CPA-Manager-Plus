package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/readiness"
)

const (
	defaultRuntimeAddr = "127.0.0.1:18318"
	defaultCPAAddr     = "127.0.0.1:8317"
	shutdownTimeout    = 10 * time.Second
	// The ordinary Server WriteTimeout remains short. The two long-running
	// typed update routes receive distinct, still-bounded response budgets.
	prepareUpdateResponseTimeout  = 12 * time.Minute
	prepareUpdateResponsePath     = "/v1/runtime/operations/prepare-update"
	activateUpdateResponseTimeout = 150 * time.Second
	activateUpdateResponsePath    = "/v1/runtime/operations/activate-update"
)

type config struct {
	privateConfiguration bool
	addr                 string
	runtimeIdentity      string
	runtimeGeneration    uint64
	token                string
	journalPath          string
	cpaExecutable        string
	cpaArtifactManifest  string
	cpaAddr              string
	cpaListenLoopback    bool
	cpaIdentity          *cpaprocess.ChildIdentity
}

type generationSource func() (uint64, error)

func main() {
	cfg, err := loadConfig(os.Getenv, randomRuntimeGeneration)
	if err != nil {
		log.Fatalf("configure runtime supervisor: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		log.Fatalf("runtime supervisor: %v", err)
	}
}

func loadConfig(getenv func(string) string, nextGeneration generationSource) (config, error) {
	if flag := getenv("CPAMP_PRIVATE_CONFIG_AUTHORITY"); flag != "" && flag != "1" {
		return config{}, errors.New("CPAMP_PRIVATE_CONFIG_AUTHORITY must be unset or 1")
	}
	addr := strings.TrimSpace(getenv("CPAMP_RUNTIME_ADDR"))
	if addr == "" {
		addr = defaultRuntimeAddr
	}
	identity := strings.TrimSpace(getenv("CPAMP_RUNTIME_IDENTITY"))
	if identity == "" {
		return config{}, errors.New("CPAMP_RUNTIME_IDENTITY is required")
	}
	token := getenv("CPAMP_RUNTIME_TOKEN")
	if strings.TrimSpace(token) == "" {
		return config{}, errors.New("CPAMP_RUNTIME_TOKEN is required")
	}
	if strings.IndexFunc(token, unicode.IsSpace) >= 0 {
		return config{}, errors.New("CPAMP_RUNTIME_TOKEN must not contain whitespace")
	}
	journalPath := strings.TrimSpace(getenv("CPAMP_RUNTIME_JOURNAL_PATH"))
	executable := strings.TrimSpace(getenv("CPAMP_CPA_EXECUTABLE"))
	artifactManifest := strings.TrimSpace(getenv("CPAMP_CPA_ARTIFACT_MANIFEST"))
	if err := validateLifecycleConfig(journalPath, executable, artifactManifest); err != nil {
		return config{}, err
	}
	cpaAddr := strings.TrimSpace(getenv("CPAMP_RUNTIME_CPA_ADDR"))
	if cpaAddr == "" {
		cpaAddr = defaultCPAAddr
	}
	if err := readiness.ValidateAddress(cpaAddr); err != nil {
		return config{}, fmt.Errorf("CPAMP_RUNTIME_CPA_ADDR: %w", err)
	}
	var cpaListenLoopback bool
	switch strings.ToLower(strings.TrimSpace(getenv("CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK"))) {
	case "":
	case "1", "true":
		cpaListenLoopback = true
	default:
		return config{}, errors.New("CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK: must be 1 or true")
	}
	cpaIdentity, err := loadCPAChildIdentity(getenv)
	if err != nil {
		return config{}, err
	}
	var generation uint64
	for generation == 0 {
		var err error
		generation, err = nextGeneration()
		if err != nil {
			return config{}, fmt.Errorf("generate runtime generation: %w", err)
		}
	}
	return config{
		privateConfiguration: getenv("CPAMP_PRIVATE_CONFIG_AUTHORITY") == "1",
		addr:                 addr,
		runtimeIdentity:      identity,
		runtimeGeneration:    generation,
		token:                token,
		journalPath:          journalPath,
		cpaExecutable:        executable,
		cpaArtifactManifest:  artifactManifest,
		cpaAddr:              cpaAddr,
		cpaListenLoopback:    cpaListenLoopback,
		cpaIdentity:          cpaIdentity,
	}, nil
}

func loadCPAChildIdentity(getenv func(string) string) (*cpaprocess.ChildIdentity, error) {
	uidText := strings.TrimSpace(getenv("CPAMP_RUNTIME_CPA_UID"))
	gidText := strings.TrimSpace(getenv("CPAMP_RUNTIME_CPA_GID"))
	if uidText == "" && gidText == "" {
		return nil, nil
	}
	if uidText == "" || gidText == "" {
		return nil, errors.New("CPAMP_RUNTIME_CPA_UID and CPAMP_RUNTIME_CPA_GID must be configured together")
	}
	uid, err := strconv.ParseUint(uidText, 10, 32)
	if err != nil || uid == 0 {
		return nil, errors.New("CPAMP_RUNTIME_CPA_UID must be a non-zero uint32")
	}
	gid, err := strconv.ParseUint(gidText, 10, 32)
	if err != nil || gid == 0 {
		return nil, errors.New("CPAMP_RUNTIME_CPA_GID must be a non-zero uint32")
	}
	identity := cpaprocess.ChildIdentity{UID: uint32(uid), GID: uint32(gid)}
	return &identity, nil
}

func randomRuntimeGeneration() (uint64, error) {
	var encoded [8]byte
	if _, err := rand.Read(encoded[:]); err != nil {
		return 0, fmt.Errorf("read secure random bytes: %w", err)
	}
	return binary.BigEndian.Uint64(encoded[:]), nil
}

func run(ctx context.Context, cfg config) (err error) {
	constructor := newRuntimeHandler
	if cfg.privateConfiguration {
		constructor = newPrivateRuntimeHandler
	}
	handler, err := constructor(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, handler.Close()) }()
	if err := enableArtifactRoutes(cfg, handler); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.addr, err)
	}
	log.Printf("cpamp-runtime-supervisor listening on %s", listener.Addr())
	return serve(ctx, listener, handler)
}

func serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	return serveWithShutdownTimeout(ctx, listener, handler, shutdownTimeout)
}

type lifecycleAdmissionCloser interface {
	CloseAdmission()
}

func serveWithShutdownTimeout(ctx context.Context, listener net.Listener, handler http.Handler, timeout time.Duration) error {
	server := newHTTPServer(handler)
	serveResult := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveResult <- err
	}()

	select {
	case err := <-serveResult:
		return err
	case <-ctx.Done():
	}

	if lifecycle, ok := handler.(lifecycleAdmissionCloser); ok {
		lifecycle.CloseAdmission()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	var closeErr error
	if shutdownErr != nil {
		closeErr = server.Close()
		if errors.Is(closeErr, http.ErrServerClosed) {
			closeErr = nil
		}
	}
	serveErr := <-serveResult
	if shutdownErr != nil || closeErr != nil || serveErr != nil {
		return errors.Join(shutdownErr, closeErr, serveErr)
	}
	return nil
}

func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           withPrepareUpdateResponseBudget(handler),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}

func withPrepareUpdateResponseBudget(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || (r.URL.Path != prepareUpdateResponsePath && r.URL.Path != activateUpdateResponsePath) {
			next.ServeHTTP(w, r)
			return
		}
		responseTimeout := prepareUpdateResponseTimeout
		operationName := "prepare-update"
		if r.URL.Path == activateUpdateResponsePath {
			responseTimeout = activateUpdateResponseTimeout
			operationName = "activate-update"
		}
		controller := http.NewResponseController(w)
		if err := controller.SetWriteDeadline(time.Now().Add(responseTimeout)); err != nil {
			// A real net/http server supports this controller. Fail closed when a
			// custom writer cannot provide the route-specific deadline instead of
			// silently disabling the bounded prepare contract.
			http.Error(w, operationName+" response deadline unavailable", http.StatusInternalServerError)
			return
		}
		defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
		next.ServeHTTP(w, r)
	})
}
