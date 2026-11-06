package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
)

func TestLoadConfigCPAAddressDefaultsAndValidation(t *testing.T) {
	for _, test := range []struct{ address, want string }{
		{"", "127.0.0.1:8317"},
		{" 127.0.0.2:18417 ", "127.0.0.2:18417"},
		{"localhost:8318", "localhost:8318"},
		{"[::1]:8318", "[::1]:8318"},
	} {
		values := validConfigValues()
		values["CPAMP_RUNTIME_CPA_ADDR"] = test.address
		cfg, err := loadConfig(func(key string) string { return values[key] }, fixedGeneration(41))
		if err != nil || cfg.cpaAddr != test.want {
			t.Fatalf("CPA address %q = %q, %v", test.address, cfg.cpaAddr, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8317", "remote.invalid:8317", "localhost:0", "[::]:8317", "http://localhost:8317/path"} {
		t.Run(address, func(t *testing.T) {
			values := validConfigValues()
			values["CPAMP_RUNTIME_CPA_ADDR"] = address
			if _, err := loadConfig(func(key string) string { return values[key] }, fixedGeneration(41)); err == nil ||
				!strings.Contains(err.Error(), "CPAMP_RUNTIME_CPA_ADDR") {
				t.Fatalf("invalid CPA address accepted: %v", err)
			}
			cfg := loadTestConfig(t, fixedGeneration(41))
			cfg.cpaAddr = address
			cfg.journalPath = filepath.Join(t.TempDir(), "private", "operations.sqlite")
			cfg.cpaExecutable = "unused-cpa"
			if handler, err := newRuntimeHandler(t.Context(), cfg); handler != nil || err == nil {
				t.Fatalf("startup accepted invalid CPA address: %v", err)
			}
			if _, err := os.Stat(filepath.Dir(cfg.journalPath)); !os.IsNotExist(err) {
				t.Fatal("invalid probe configuration opened the journal before failing")
			}
		})
	}
}

func TestDestinationListenLoopbackRejectsInvalidValue(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want bool
	}{
		{"", false},
		{"1", true},
		{" 1 ", true},
		{"true", true},
		{"TRUE", true},
		{" True ", true},
	} {
		values := validConfigValues()
		values["CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK"] = test.raw
		cfg, err := loadConfig(func(key string) string { return values[key] }, fixedGeneration(41))
		if err != nil || cfg.cpaListenLoopback != test.want {
			t.Fatalf("loadConfig(CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK=%q) = %v, %v", test.raw, cfg.cpaListenLoopback, err)
		}
	}
	for _, raw := range []string{"yes", "0", "on", "false", "2"} {
		t.Run(raw, func(t *testing.T) {
			values := validConfigValues()
			values["CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK"] = raw
			_, err := loadConfig(func(key string) string { return values[key] }, fixedGeneration(41))
			if err == nil || !strings.Contains(err.Error(), "CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK: must be 1 or true") {
				t.Fatalf("loadConfig(CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK=%q) error = %v", raw, err)
			}
		})
	}
}

func TestDestinationListenLoopbackRequiresExactLoopbackHost(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8317", "[::1]:8317", "localhost:8317"} {
		t.Run("accept_"+address, func(t *testing.T) {
			directory := t.TempDir()
			values := validConfigValues()
			values["CPAMP_RUNTIME_CPA_ADDR"] = address
			values["CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK"] = "1"
			values["CPAMP_RUNTIME_JOURNAL_PATH"] = filepath.Join(directory, "runtime", "operations.sqlite")
			values["CPAMP_CPA_EXECUTABLE"] = copyStartHelper(t, directory)
			cfg, err := loadConfig(func(key string) string { return values[key] }, fixedGeneration(41))
			if err != nil || !cfg.cpaListenLoopback {
				t.Fatalf("loadConfig(%q) = %+v, %v", address, cfg, err)
			}
			handler, err := newRuntimeHandler(t.Context(), cfg)
			if err != nil {
				t.Fatalf("newRuntimeHandler(%q) error = %v", address, err)
			}
			if err := handler.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, address := range []string{"127.0.0.2:8317", "127.0.1.1:8317", "0.0.0.0:8317", "192.0.2.10:8317"} {
		t.Run("reject_"+address, func(t *testing.T) {
			directory := t.TempDir()
			cfg := loadTestConfig(t, fixedGeneration(41))
			cfg.cpaAddr = address
			cfg.cpaListenLoopback = true
			cfg.journalPath = filepath.Join(directory, "private", "operations.sqlite")
			cfg.cpaExecutable = copyStartHelper(t, directory)
			handler, err := newRuntimeHandler(t.Context(), cfg)
			if handler != nil || err == nil || !strings.Contains(err.Error(), "CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK") {
				t.Fatalf("newRuntimeHandler(%q) = %v, %v; want CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK error", address, handler, err)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(cfg.journalPath), "destination")); !os.IsNotExist(err) {
				t.Fatalf("rejected loopback address %q created destination directory: %v", address, err)
			}
			if _, err := os.Stat(filepath.Dir(cfg.journalPath)); !os.IsNotExist(err) {
				t.Fatalf("rejected loopback address %q opened supervisor runtime root before failing: %v", address, err)
			}
			if _, err := os.Stat(filepath.Join(directory, "spawns")); !os.IsNotExist(err) {
				t.Fatalf("rejected loopback address %q started a child process: %v", address, err)
			}
		})
	}
}

func TestReadOnlyRuntimeRemainsUnknownWithoutCPAProbe(t *testing.T) {
	var probes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		probes.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	cfg := loadTestConfig(t, fixedGeneration(41))
	cfg.cpaAddr = server.Listener.Addr().String()
	handler, err := newRuntimeHandler(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	for _, path := range []string{"/v1/runtime/handshake", "/v1/runtime/status"} {
		got := requestRuntime(t, handler, path)
		if got.RuntimeGeneration != 41 || len(got.Capabilities) != 0 || got.CPAObservedVersion != "" ||
			(path == "/v1/runtime/status" && got.State != "unknown") {
			t.Fatalf("read-only status changed: %+v", got)
		}
	}
	if probes.Load() != 0 {
		t.Fatal("read-only runtime probed CPA")
	}
}

func TestReadinessTransitionsDoNotChangeLifecycleEvidence(t *testing.T) {
	var ready atomic.Bool
	var probes atomic.Int32
	handler, cfg, directory := newReadinessRuntime(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		if r.Method != http.MethodHead || r.URL.String() != "/healthz" {
			t.Errorf("unexpected CPA probe %s %s", r.Method, r.URL)
		}
		if ready.Load() {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	assertReadiness(t, handler, "offline")
	requestRuntime(t, handler, "/v1/runtime/handshake")
	if probes.Load() != 0 {
		t.Fatal("not_started or handshake probed CPA")
	}
	reader, err := journal.Open(t.Context(), cfg.journalPath, journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	for index, operationType := range []string{"start", "restart"} {
		ready.Store(false)
		probeCount := probes.Load()
		var result *httptest.ResponseRecorder
		if operationType == "start" {
			result = runtimeStart(handler, t.Context(), operationType, 41)
		} else {
			result = runtimeRestart(handler, t.Context(), operationType, 41)
		}
		if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"state":"succeeded"`) {
			t.Fatalf("%s did not succeed independently of readiness: %d %s", operationType, result.Code, result.Body)
		}
		awaitSpawnCount(t, directory, index+1)
		if probes.Load() != probeCount {
			t.Fatalf("%s performed a readiness probe", operationType)
		}
		stored, err := reader.Get(t.Context(), cfg.runtimeIdentity, operationType)
		if err != nil || stored.State != journal.StateSucceeded {
			t.Fatalf("operation evidence = %+v, %v", stored, err)
		}
		assertReadiness(t, handler, "starting")
		ready.Store(true)
		assertReadiness(t, handler, "ready")
		// This is an observation, not a cached state or a lifecycle result.
		ready.Store(false)
		assertReadiness(t, handler, "starting")
		ready.Store(true)
		assertReadiness(t, handler, "ready")
		after, err := reader.Get(t.Context(), cfg.runtimeIdentity, operationType)
		if err != nil || !reflect.DeepEqual(stored, after) {
			t.Fatalf("readiness modified durable %s evidence: before %+v, after %+v, %v", operationType, stored, after, err)
		}
		var replay *httptest.ResponseRecorder
		if operationType == "start" {
			replay = runtimeStart(handler, t.Context(), operationType, 41)
		} else {
			replay = runtimeRestart(handler, t.Context(), operationType, 41)
		}
		if replay.Code != http.StatusOK || replay.Body.String() != result.Body.String() {
			t.Fatalf("%s replay changed after readiness", operationType)
		}
		awaitSpawnCount(t, directory, index+1)
	}
	probeCount := probes.Load()
	if stopped := runtimeStop(handler, t.Context(), "stop", 41); stopped.Code != http.StatusOK {
		t.Fatalf("Stop = %d %s", stopped.Code, stopped.Body)
	}
	assertReadiness(t, handler, "offline")
	if probes.Load() != probeCount {
		t.Fatal("Stop or confirmed exited status probed CPA")
	}
	stored, err := reader.Get(t.Context(), cfg.runtimeIdentity, "stop")
	if err != nil || stored.State != journal.StateSucceeded {
		t.Fatalf("Stop evidence = %+v, %v", stored, err)
	}
}

func TestRuntimeRestartFencesAnInFlightOldChildProbe(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var probes atomic.Int32
	var expired atomic.Bool
	handler, _, directory := newReadinessRuntime(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if probes.Add(1) == 1 {
			close(entered)
			<-release
			expired.Store(r.Context().Err() != nil)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer unblock()
	if started := runtimeStart(handler, t.Context(), "start-A", 41); started.Code != http.StatusOK {
		t.Fatalf("Start A = %s", started.Body)
	}
	awaitSpawnCount(t, directory, 1)
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodGet, "/v1/runtime/status", nil)
		req.Header.Set("Authorization", "Bearer runtime-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		result <- response
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("old child's probe did not reach listener")
	}
	if restarted := runtimeRestart(handler, t.Context(), "restart-B", 41); restarted.Code != http.StatusOK {
		t.Fatalf("Restart B = %s", restarted.Body)
	}
	// Restart completes while A's probe is still blocked; the old result cannot
	// be evidence for B. No observer lock may serialize this network call with mutation.
	unblock()
	response := <-result
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"starting"`) {
		t.Fatalf("old probe marked replacement ready: %d %s", response.Code, response.Body)
	}
	if expired.Load() {
		t.Fatal("stale-probe test timed out before delivering the old HTTP 200")
	}
	awaitSpawnCount(t, directory, 2)
	assertReadiness(t, handler, "ready")
	if probes.Load() != 2 {
		t.Fatalf("probes = %d, want one for A and one for B", probes.Load())
	}
}

func unreadyCPAAddr(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	t.Cleanup(server.Close)
	return server.Listener.Addr().String()
}

func newReadinessRuntime(t *testing.T, cpa http.Handler) (*runtimeHandler, config, string) {
	t.Helper()
	server := httptest.NewServer(cpa)
	t.Cleanup(server.Close)
	directory := t.TempDir()
	values := validConfigValues()
	values["CPAMP_RUNTIME_CPA_ADDR"] = server.Listener.Addr().String()
	values["CPAMP_RUNTIME_JOURNAL_PATH"] = filepath.Join(directory, "runtime", "operations.sqlite")
	values["CPAMP_CPA_EXECUTABLE"] = copyStartHelper(t, directory)
	for _, key := range []string{"NORMAL_SENTINEL", "CPAMP_DEPLOYMENT_SENTINEL", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"} {
		values[key] = "test-only-ordinary-value"
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	cfg, err := loadConfig(os.Getenv, fixedGeneration(41))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newRuntimeHandler(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		runtimeStop(handler, context.Background(), "fixture-cleanup-stop", 41)
		if err := handler.Close(); err != nil {
			t.Error(err)
		}
	})
	return handler, cfg, directory
}

func assertReadiness(t *testing.T, handler http.Handler, state string) {
	t.Helper()
	got := requestRuntime(t, handler, "/v1/runtime/status")
	if got.State != state || got.CPAObservedVersion != "" || got.RuntimeIdentity != "runtime-01" || got.RuntimeGeneration != 41 ||
		got.ProtocolVersion != "v1" || !reflect.DeepEqual(got.Capabilities, expectedRuntimeCapabilities()) {
		t.Fatalf("status = %+v, want %s with unchanged authority/capabilities and no invented version", got, state)
	}
}
