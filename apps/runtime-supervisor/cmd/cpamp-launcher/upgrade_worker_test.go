//go:build unix

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestUpgradeWorkerAdmissionDoesNotFailOpen(t *testing.T) {
	for _, input := range []string{
		`{}`, `null`, `{"required":null}`, `{"required":"false"}`,
		`{"required":false,"stage":"unknown"}`, `{"required":false}{"required":true}`,
		`{"required":false} incomplete`,
	} {
		if _, err := decodeUpgradeWorkerCheck([]byte(input)); err == nil {
			t.Errorf("ambiguous operation admission was accepted: %s", input)
		}
	}
	for _, state := range []struct {
		document string
		required bool
	}{
		{`{"required":true}`, true},
		{`{"required":false}`, false},
	} {
		required, err := decodeUpgradeWorkerCheck([]byte(state.document))
		if err != nil || required != state.required {
			t.Errorf("operation admission = %t, %v for %s", required, err, state.document)
		}
	}
}

func TestUpgradeWorkerCannotOutliveLauncherShutdown(t *testing.T) {
	eventFile := filepath.Join(t.TempDir(), "events.log")
	supAddr := reserveLoopbackAddr(t)
	managerAddr := reserveLoopbackAddr(t)
	worker := helperChildSpec(t, "upgrade-worker", "CPAMP_TEST_EVENT_FILE="+eventFile)
	cfg := SupervisorConfig{
		SupervisorSpec:    helperChildSpec(t, "supervisor", "CPAMP_TEST_EVENT_FILE="+eventFile, "CPAMP_TEST_TCP_LISTEN_ADDR="+supAddr),
		ManagerSpec:       helperChildSpec(t, "manager", "CPAMP_TEST_EVENT_FILE="+eventFile, "CPAMP_TEST_HTTP_LISTEN_ADDR="+managerAddr),
		IngressSpec:       helperChildSpec(t, "ingress", "CPAMP_TEST_EVENT_FILE="+eventFile),
		UpgradeSpec:       &worker,
		SupervisorTCPAddr: supAddr,
		ManagerHealthURL:  "http://" + managerAddr + "/health",
		Timings:           fastTimings(),
		Stdout:            io.Discard,
		Stderr:            io.Discard,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunSupervisor(ctx, cfg)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("launcher child cleanup did not finish")
		}
	})
	events := waitForEvents(t, eventFile, 4, func(events []recordedEvent) bool {
		roles := map[string]bool{}
		for _, event := range events {
			if event.Kind == "start" {
				roles[event.Role] = true
			}
		}
		return roles["supervisor"] && roles["upgrade-worker"] && roles["manager"] && roles["ingress"]
	})
	workerPID := 0
	for _, event := range events {
		if event.Role == "upgrade-worker" && event.Kind == "start" {
			workerPID = event.PID
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("launcher shutdown did not finish")
	}
	if workerPID == 0 {
		t.Fatal("upgrade worker never started")
	}
	if err := syscall.Kill(workerPID, 0); err != syscall.ESRCH {
		t.Fatalf("privileged upgrade worker survived shutdown: %v", err)
	}
	for _, event := range parseEvents(t, eventFile) {
		if event.Role == "upgrade-worker" && event.Kind == "sigterm" {
			return
		}
	}
	t.Fatal("upgrade worker was not included in graceful shutdown")
}

func TestUpgradeWorkerBoundedFailureStillStartsManagerAndIngress(t *testing.T) {
	t.Run("repeated_exit_1_aborts_socket_wait_and_starts_services", func(t *testing.T) {
		eventFile := filepath.Join(t.TempDir(), "events.log")
		sockDir, err := os.MkdirTemp("/tmp", "cpamp-uw-*")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
		socketPath := filepath.Join(sockDir, "worker.sock")

		supAddr := reserveLoopbackAddr(t)
		managerAddr := reserveLoopbackAddr(t)
		worker := helperChildSpec(
			t,
			"upgrade-worker",
			"CPAMP_TEST_EVENT_FILE="+eventFile,
			"CPAMP_TEST_EXIT_AFTER_MS=10",
		)
		timings := fastTimings()
		timings.InitialBackoff = 15 * time.Millisecond
		timings.MaxBackoff = 40 * time.Millisecond
		timings.UpgradeWorkerReadyTimeout = 2 * time.Second
		timings.UpgradeWorkerMaxInitialFailures = 3

		var stderrBuf lockedBuffer
		cfg := SupervisorConfig{
			SupervisorSpec:    helperChildSpec(t, "supervisor", "CPAMP_TEST_EVENT_FILE="+eventFile, "CPAMP_TEST_TCP_LISTEN_ADDR="+supAddr),
			ManagerSpec:       helperChildSpec(t, "manager", "CPAMP_TEST_EVENT_FILE="+eventFile, "CPAMP_TEST_HTTP_LISTEN_ADDR="+managerAddr),
			IngressSpec:       helperChildSpec(t, "ingress", "CPAMP_TEST_EVENT_FILE="+eventFile),
			UpgradeSpec:       &worker,
			UpgradeSocket:     socketPath,
			SupervisorTCPAddr: supAddr,
			ManagerHealthURL:  "http://" + managerAddr + "/health",
			Timings:           timings,
			Stdout:            io.Discard,
			Stderr:            &stderrBuf,
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		startedAt := time.Now()
		go func() {
			done <- RunSupervisor(ctx, cfg)
			close(done)
		}()
		t.Cleanup(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("launcher child cleanup did not finish")
			}
		})

		waitForEvents(t, eventFile, 4, func(events []recordedEvent) bool {
			roles := map[string]bool{}
			crashes := 0
			for _, event := range events {
				if event.Kind == "start" {
					roles[event.Role] = true
				}
				if event.Role == "upgrade-worker" && event.Kind == "crash" {
					crashes++
				}
			}
			return crashes >= 3 && roles["supervisor"] && roles["manager"] && roles["ingress"]
		})
		if elapsed := time.Since(startedAt); elapsed >= 1500*time.Millisecond {
			t.Fatalf("manager and ingress took %s to start; expected fast failure-count bound well below 2s timeout", elapsed)
		}

		cancel()
		select {
		case runErr := <-done:
			if runErr != nil {
				t.Fatalf("RunSupervisor returned unexpected error: %v", runErr)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("launcher shutdown did not finish")
		}
		if !strings.Contains(stderrBuf.String(), "upgrade worker not ready") {
			t.Fatalf("expected degraded upgrade worker warning in stderr, got:\n%s", stderrBuf.String())
		}
	})

	t.Run("unresponsive_socket_bounds_wait_by_timeout_and_starts_services", func(t *testing.T) {
		eventFile := filepath.Join(t.TempDir(), "events.log")
		sockDir, err := os.MkdirTemp("/tmp", "cpamp-uw-*")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
		socketPath := filepath.Join(sockDir, "worker.sock")

		supAddr := reserveLoopbackAddr(t)
		managerAddr := reserveLoopbackAddr(t)
		worker := helperChildSpec(t, "upgrade-worker", "CPAMP_TEST_EVENT_FILE="+eventFile)
		timings := fastTimings()
		timings.UpgradeWorkerReadyTimeout = 120 * time.Millisecond

		var stderrBuf lockedBuffer
		cfg := SupervisorConfig{
			SupervisorSpec:    helperChildSpec(t, "supervisor", "CPAMP_TEST_EVENT_FILE="+eventFile, "CPAMP_TEST_TCP_LISTEN_ADDR="+supAddr),
			ManagerSpec:       helperChildSpec(t, "manager", "CPAMP_TEST_EVENT_FILE="+eventFile, "CPAMP_TEST_HTTP_LISTEN_ADDR="+managerAddr),
			IngressSpec:       helperChildSpec(t, "ingress", "CPAMP_TEST_EVENT_FILE="+eventFile),
			UpgradeSpec:       &worker,
			UpgradeSocket:     socketPath,
			SupervisorTCPAddr: supAddr,
			ManagerHealthURL:  "http://" + managerAddr + "/health",
			Timings:           timings,
			Stdout:            io.Discard,
			Stderr:            &stderrBuf,
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- RunSupervisor(ctx, cfg)
			close(done)
		}()
		t.Cleanup(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("launcher child cleanup did not finish")
			}
		})

		waitForEvents(t, eventFile, 4, func(events []recordedEvent) bool {
			roles := map[string]bool{}
			for _, event := range events {
				if event.Kind == "start" {
					roles[event.Role] = true
				}
			}
			return roles["supervisor"] && roles["upgrade-worker"] && roles["manager"] && roles["ingress"]
		})

		cancel()
		select {
		case runErr := <-done:
			if runErr != nil {
				t.Fatalf("RunSupervisor returned unexpected error: %v", runErr)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("launcher shutdown did not finish")
		}
		if !strings.Contains(stderrBuf.String(), "socket not ready after") {
			t.Fatalf("expected timeout warning in stderr, got:\n%s", stderrBuf.String())
		}
	})
}
