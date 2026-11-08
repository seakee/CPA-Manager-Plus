package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if role := os.Getenv("CPAMP_TEST_HELPER_ROLE"); role != "" {
		runTestHelperProcess(role)
		return
	}
	os.Exit(m.Run())
}

func runTestHelperProcess(role string) {
	eventFile := os.Getenv("CPAMP_TEST_EVENT_FILE")
	recordEvent := func(kind string) {
		if eventFile == "" {
			return
		}
		f, err := os.OpenFile(eventFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(f, "%s:%s:%d:%d\n", kind, role, os.Getpid(), time.Now().UnixMilli())
		_ = f.Close()
	}

	recordEvent("start")
	fmt.Printf("started %s pid=%d\n", role, os.Getpid())

	if subPIDFile := os.Getenv("CPAMP_TEST_SPAWN_SUBCHILD_PID_FILE"); subPIDFile != "" {
		subCmd := exec.Command(os.Args[0])
		subCmd.Env = append(os.Environ(),
			"CPAMP_TEST_HELPER_ROLE=cpa-subchild",
			"CPAMP_TEST_SPAWN_SUBCHILD_PID_FILE=",
			"CPAMP_TEST_EXIT_AFTER_MS=",
			"CPAMP_TEST_IGNORE_SIGTERM=1",
		)
		if err := subCmd.Start(); err == nil && subCmd.Process != nil {
			_ = os.WriteFile(subPIDFile, []byte(strconv.Itoa(subCmd.Process.Pid)), 0o600)
		}
	}

	if delayMS, _ := strconv.Atoi(os.Getenv("CPAMP_TEST_READY_DELAY_MS")); delayMS > 0 {
		time.Sleep(time.Duration(delayMS) * time.Millisecond)
	}

	if tcpAddr := os.Getenv("CPAMP_TEST_TCP_LISTEN_ADDR"); tcpAddr != "" {
		ln, err := net.Listen("tcp", tcpAddr)
		if err == nil {
			defer ln.Close()
			go func() {
				for {
					conn, acceptErr := ln.Accept()
					if acceptErr != nil {
						return
					}
					_ = conn.Close()
				}
			}()
		}
	}

	if httpAddr := os.Getenv("CPAMP_TEST_HTTP_LISTEN_ADDR"); httpAddr != "" {
		ln, err := net.Listen("tcp", httpAddr)
		if err == nil {
			defer ln.Close()
			bootstrapOnly := os.Getenv("CPAMP_TEST_HTTP_BOOTSTRAP_ONLY") == "1"
			mux := http.NewServeMux()
			mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
				if bootstrapOnly {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"status":"fresh_claim_required"}`))
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("ok"))
			})
			mux.HandleFunc("/.well-known/cpamp-bootstrap", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"mode":"fresh"}`))
			})
			srv := &http.Server{Handler: mux}
			go func() { _ = srv.Serve(ln) }()
		}
	}

	if countFile := os.Getenv("CPAMP_TEST_CRASH_SEQUENCE_FILE"); countFile != "" {
		raw, _ := os.ReadFile(countFile)
		attempt, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
		attempt++
		_ = os.WriteFile(countFile, []byte(strconv.Itoa(attempt)), 0o600)

		// Format of CPAMP_TEST_CRASH_DURATIONS_MS: comma-separated lifetimes per attempt (e.g. "15,15,220,0")
		// where 0 means stay running.
		durations := strings.Split(os.Getenv("CPAMP_TEST_CRASH_DURATIONS_MS"), ",")
		if attempt-1 < len(durations) {
			if lifeMS, err := strconv.Atoi(strings.TrimSpace(durations[attempt-1])); err == nil && lifeMS > 0 {
				time.Sleep(time.Duration(lifeMS) * time.Millisecond)
				recordEvent("crash")
				os.Exit(1)
			}
		}
	}

	if exitMS, _ := strconv.Atoi(os.Getenv("CPAMP_TEST_EXIT_AFTER_MS")); exitMS > 0 {
		time.Sleep(time.Duration(exitMS) * time.Millisecond)
		recordEvent("crash")
		os.Exit(1)
	}

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	for range sigCh {
		recordEvent("sigterm")
		if os.Getenv("CPAMP_TEST_IGNORE_SIGTERM") == "1" {
			continue
		}
		if termDelayMS, _ := strconv.Atoi(os.Getenv("CPAMP_TEST_SIGTERM_DELAY_MS")); termDelayMS > 0 {
			time.Sleep(time.Duration(termDelayMS) * time.Millisecond)
		}
		recordEvent("exit")
		os.Exit(0)
	}
}

func reserveLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen loopback: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func helperChildSpec(t *testing.T, name string, extraEnv ...string) ChildSpec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=/",
		"CPAMP_TEST_HELPER_ROLE=" + name,
	}
	env = append(env, extraEnv...)
	return ChildSpec{
		Name:       name,
		Prefix:     "[" + name + "] ",
		Executable: exe,
		Dir:        t.TempDir(),
		Env:        env,
	}
}

func fastTimings() TimingConfig {
	return TimingConfig{
		InitialBackoff:                        30 * time.Millisecond,
		MaxBackoff:                            200 * time.Millisecond,
		HealthyUptimeReset:                    150 * time.Millisecond,
		SupervisorShortRunWindow:              120 * time.Millisecond,
		SupervisorMaxConsecutiveShortFailures: 5,
		DrainTimeout:                          80 * time.Millisecond,
		IngressDrainGraceExtra:                30 * time.Millisecond,
		ManagerShutdownTimeout:                100 * time.Millisecond,
		SupervisorShutdownTimeout:             100 * time.Millisecond,
		ProcessGroupKillGrace:                 60 * time.Millisecond,
		ReadinessWaitTimeout:                  1500 * time.Millisecond,
		ReadinessPollInterval:                 15 * time.Millisecond,
	}
}

type recordedEvent struct {
	Kind   string
	Role   string
	PID    int
	TimeMS int64
}

func parseEvents(t *testing.T, eventFile string) []recordedEvent {
	t.Helper()
	data, err := os.ReadFile(eventFile)
	if err != nil {
		t.Fatalf("read event file: %v", err)
	}
	var out []recordedEvent
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) != 4 {
			continue
		}
		pid, _ := strconv.Atoi(parts[2])
		ts, _ := strconv.ParseInt(parts[3], 10, 64)
		out = append(out, recordedEvent{
			Kind:   parts[0],
			Role:   parts[1],
			PID:    pid,
			TimeMS: ts,
		})
	}
	return out
}

func waitForEvents(t *testing.T, eventFile string, minCount int, match func([]recordedEvent) bool) []recordedEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(eventFile); err == nil {
			evs := parseEvents(t, eventFile)
			if len(evs) >= minCount && (match == nil || match(evs)) {
				return evs
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	evs, _ := os.ReadFile(eventFile)
	t.Fatalf("timed out waiting for events in %s; got:\n%s", eventFile, string(evs))
	return nil
}

func TestChildEnvironmentBuilders(t *testing.T) {
	prepared := &PreparedRuntime{
		DataDir:                 "/data",
		RuntimeRoot:             "/data/runtime",
		JournalPath:             "/data/runtime/supervisor/operations.sqlite",
		GatewayDir:              "/data/runtime/gateway",
		TokenFile:               "/data/runtime-secret/token",
		RuntimeToken:            strings.Repeat("a", 64),
		CPAUID:                  10001,
		CPAGID:                  10001,
		ManagerUID:              10002,
		ManagerGID:              10002,
		IngressUID:              10003,
		IngressGID:              10003,
		RuntimeAddr:             "127.0.0.1:9081",
		RuntimeIdentity:         "embedded-default",
		CPAExecutablePath:       "/usr/local/bin/cli-proxy-api",
		CPAArtifactManifestPath: "/usr/local/share/cpamp/runtime/cpa-artifact.json",
		RuntimeCPAAddr:          "127.0.0.1:8317",
	}
	containerEnv := map[string]string{
		"PATH":                  "/usr/local/bin:/usr/bin:/bin",
		"TZ":                    "UTC",
		"CPAMP_RUNTIME_TOKEN":   strings.Repeat("a", 64),
		"CPAMP_PUBLIC_ORIGIN":   "https://cpamp.example.com",
		"USAGE_BATCH_SIZE":      "250",
		"USAGE_CUSTOM_OVERRIDE": "enabled",
		"UNRELATED_HOST_SECRET": "must-not-leak",
	}

	supEnv := parseEnvironMap(BuildSupervisorChildEnv(prepared, containerEnv))
	if supEnv["CPAMP_RUNTIME_TOKEN"] != prepared.RuntimeToken {
		t.Fatalf("supervisor missing CPAMP_RUNTIME_TOKEN")
	}
	if _, leaked := supEnv["UNRELATED_HOST_SECRET"]; leaked {
		t.Fatalf("supervisor leaked UNRELATED_HOST_SECRET")
	}

	mgrEnv := parseEnvironMap(BuildManagerChildEnv(prepared, containerEnv))
	if _, leaked := mgrEnv["CPAMP_RUNTIME_TOKEN"]; leaked {
		t.Fatalf("manager must never receive CPAMP_RUNTIME_TOKEN")
	}
	if _, leaked := mgrEnv["UNRELATED_HOST_SECRET"]; leaked {
		t.Fatalf("manager leaked UNRELATED_HOST_SECRET")
	}
	if mgrEnv["HTTP_ADDR"] != "127.0.0.1:18319" ||
		mgrEnv["CPAMP_RUNTIME_TOKEN_FILE"] != "/data/runtime-secret/token" ||
		mgrEnv["CPAMP_INGRESS_CONTROL_URL"] != "http://127.0.0.1:9082" ||
		mgrEnv["USAGE_BATCH_SIZE"] != "250" ||
		mgrEnv["USAGE_COLLECTOR_MODE"] != "auto" ||
		mgrEnv["USAGE_CUSTOM_OVERRIDE"] != "enabled" ||
		mgrEnv["CPAMP_PUBLIC_ORIGIN"] != "https://cpamp.example.com" ||
		mgrEnv["TZ"] != "UTC" {
		t.Fatalf("unexpected manager env: %#v", mgrEnv)
	}

	ingEnv := parseEnvironMap(BuildIngressChildEnv(90*time.Second, containerEnv))
	if _, leaked := ingEnv["CPAMP_RUNTIME_TOKEN"]; leaked {
		t.Fatalf("ingress must never receive CPAMP_RUNTIME_TOKEN")
	}
	if ingEnv["CPAMP_INGRESS_ADDR"] != "0.0.0.0:18317" ||
		ingEnv["CPAMP_MANAGER_URL"] != "http://127.0.0.1:18319" ||
		ingEnv["CPAMP_GATEWAY_URL"] != "http://127.0.0.1:8317" ||
		ingEnv["CPAMP_INGRESS_CONTROL_ADDR"] != "127.0.0.1:9082" ||
		ingEnv["CPAMP_INGRESS_SHUTDOWN_TIMEOUT"] != "1m30s" {
		t.Fatalf("unexpected ingress env: %#v", ingEnv)
	}
}

func TestStartOrderAndReadinessGating(t *testing.T) {
	eventFile := filepath.Join(t.TempDir(), "events.log")
	supAddr := reserveLoopbackAddr(t)
	mgrAddr := reserveLoopbackAddr(t)

	var outBuf bytes.Buffer
	cfg := SupervisorConfig{
		SupervisorSpec: helperChildSpec(t, "supervisor",
			"CPAMP_TEST_EVENT_FILE="+eventFile,
			"CPAMP_TEST_READY_DELAY_MS=70",
			"CPAMP_TEST_TCP_LISTEN_ADDR="+supAddr,
		),
		ManagerSpec: helperChildSpec(t, "manager",
			"CPAMP_TEST_EVENT_FILE="+eventFile,
			"CPAMP_TEST_READY_DELAY_MS=70",
			"CPAMP_TEST_HTTP_LISTEN_ADDR="+mgrAddr,
		),
		IngressSpec: helperChildSpec(t, "ingress",
			"CPAMP_TEST_EVENT_FILE="+eventFile,
		),
		SupervisorTCPAddr: supAddr,
		ManagerHealthURL:  "http://" + mgrAddr + "/health",
		Timings:           fastTimings(),
		Stdout:            &outBuf,
		Stderr:            &outBuf,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunSupervisor(ctx, cfg)
	}()

	evs := waitForEvents(t, eventFile, 3, func(events []recordedEvent) bool {
		starts := 0
		for _, e := range events {
			if e.Kind == "start" {
				starts++
			}
		}
		return starts >= 3
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunSupervisor returned error: %v", err)
	}

	var starts []recordedEvent
	for _, e := range evs {
		if e.Kind == "start" {
			starts = append(starts, e)
		}
	}
	if len(starts) < 3 || starts[0].Role != "supervisor" || starts[1].Role != "manager" || starts[2].Role != "ingress" {
		t.Fatalf("unexpected start order: %+v", starts)
	}
	if starts[1].TimeMS-starts[0].TimeMS < 50 {
		t.Fatalf("manager started before supervisor readiness delay (%dms)", starts[1].TimeMS-starts[0].TimeMS)
	}
	if starts[2].TimeMS-starts[1].TimeMS < 50 {
		t.Fatalf("ingress started before manager readiness delay (%dms)", starts[2].TimeMS-starts[1].TimeMS)
	}

	output := outBuf.String()
	for _, prefix := range []string{"[supervisor] started supervisor", "[manager] started manager", "[ingress] started ingress"} {
		if !strings.Contains(output, prefix) {
			t.Fatalf("missing prefixed line %q in output:\n%s", prefix, output)
		}
	}
}

func TestCrashRestartBackoffAndReset(t *testing.T) {
	// 1. Deterministic unit test of the pure backoff + healthy-uptime reset policy.
	policyTimings := (TimingConfig{}).withDefaults(nil)
	backoff := policyTimings.InitialBackoff

	var gotWaits []time.Duration
	for _, uptime := range []time.Duration{
		500 * time.Millisecond, // crash 1 -> wait 1s
		2 * time.Second,        // crash 2 -> wait 2s
		5 * time.Second,        // crash 3 -> wait 4s
		1 * time.Second,        // crash 4 -> wait 8s
		1 * time.Second,        // crash 5 -> wait 16s
		1 * time.Second,        // crash 6 -> wait 30s (capped at MaxRestartBackoff)
		1 * time.Second,        // crash 7 -> wait 30s (remains capped)
		60 * time.Second,       // healthy uptime >= 60s -> resets to 1s!
		1 * time.Second,        // crash after reset -> wait 2s
	} {
		var waitDur time.Duration
		waitDur, backoff = nextRestartBackoff(backoff, uptime, policyTimings)
		gotWaits = append(gotWaits, waitDur)
	}

	wantWaits := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
		30 * time.Second,
		1 * time.Second,
		2 * time.Second,
	}
	if len(gotWaits) != len(wantWaits) {
		t.Fatalf("got %d waits, want %d", len(gotWaits), len(wantWaits))
	}
	for i := range len(wantWaits) {
		if gotWaits[i] != wantWaits[i] {
			t.Fatalf("step %d: wait=%v, want %v (all=%v)", i, gotWaits[i], wantWaits[i], gotWaits)
		}
	}

	// 2. Real-process restart check asserting that a crashed child is restarted.
	eventFile := filepath.Join(t.TempDir(), "events.log")
	seqFile := filepath.Join(t.TempDir(), "seq.txt")

	cfg := SupervisorConfig{
		SupervisorSpec: helperChildSpec(t, "supervisor", "CPAMP_TEST_EVENT_FILE="+eventFile),
		ManagerSpec:    helperChildSpec(t, "manager", "CPAMP_TEST_EVENT_FILE="+eventFile),
		IngressSpec: helperChildSpec(t, "ingress",
			"CPAMP_TEST_EVENT_FILE="+eventFile,
			"CPAMP_TEST_CRASH_SEQUENCE_FILE="+seqFile,
			"CPAMP_TEST_CRASH_DURATIONS_MS=15,15,0",
		),
		Timings: fastTimings(),
		Stdout:  &bytes.Buffer{},
		Stderr:  &bytes.Buffer{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunSupervisor(ctx, cfg)
	}()

	evs := waitForEvents(t, eventFile, 7, func(events []recordedEvent) bool {
		ingStarts := 0
		for _, e := range events {
			if e.Role == "ingress" && e.Kind == "start" {
				ingStarts++
			}
		}
		return ingStarts >= 3
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunSupervisor failed: %v", err)
	}

	ingStarts := 0
	ingCrashes := 0
	for _, e := range evs {
		if e.Role == "ingress" {
			switch e.Kind {
			case "start":
				ingStarts++
			case "crash":
				ingCrashes++
			}
		}
	}
	if ingStarts < 3 || ingCrashes < 2 {
		t.Fatalf("expected >=3 starts and >=2 crashes, got starts=%d crashes=%d", ingStarts, ingCrashes)
	}
}

func TestManagerCrashDoesNotStopOthers(t *testing.T) {
	eventFile := filepath.Join(t.TempDir(), "events.log")
	seqFile := filepath.Join(t.TempDir(), "mgr-seq.txt")

	cfg := SupervisorConfig{
		SupervisorSpec: helperChildSpec(t, "supervisor", "CPAMP_TEST_EVENT_FILE="+eventFile),
		ManagerSpec: helperChildSpec(t, "manager",
			"CPAMP_TEST_EVENT_FILE="+eventFile,
			"CPAMP_TEST_CRASH_SEQUENCE_FILE="+seqFile,
			"CPAMP_TEST_CRASH_DURATIONS_MS=25,0",
		),
		IngressSpec: helperChildSpec(t, "ingress", "CPAMP_TEST_EVENT_FILE="+eventFile),
		Timings:     fastTimings(),
		Stdout:      &bytes.Buffer{},
		Stderr:      &bytes.Buffer{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunSupervisor(ctx, cfg)
	}()

	evs := waitForEvents(t, eventFile, 5, func(events []recordedEvent) bool {
		mgrStarts := 0
		for _, e := range events {
			if e.Role == "manager" && e.Kind == "start" {
				mgrStarts++
			}
		}
		return mgrStarts >= 2
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunSupervisor failed: %v", err)
	}

	supStarts := 0
	ingStarts := 0
	mgrStarts := 0
	for _, e := range evs {
		if e.Kind == "start" {
			switch e.Role {
			case "supervisor":
				supStarts++
			case "ingress":
				ingStarts++
			case "manager":
				mgrStarts++
			}
		}
	}
	if supStarts != 1 || ingStarts != 1 || mgrStarts < 2 {
		t.Fatalf("expected supervisor=1 ingress=1 manager>=2 starts, got sup=%d ing=%d mgr=%d", supStarts, ingStarts, mgrStarts)
	}
}

func TestSIGTERMDrainOrderIngressManagerSupervisor(t *testing.T) {
	eventFile := filepath.Join(t.TempDir(), "events.log")

	cfg := SupervisorConfig{
		SupervisorSpec: helperChildSpec(t, "supervisor",
			"CPAMP_TEST_EVENT_FILE="+eventFile,
			"CPAMP_TEST_SIGTERM_DELAY_MS=25",
		),
		ManagerSpec: helperChildSpec(t, "manager",
			"CPAMP_TEST_EVENT_FILE="+eventFile,
			"CPAMP_TEST_SIGTERM_DELAY_MS=25",
		),
		IngressSpec: helperChildSpec(t, "ingress",
			"CPAMP_TEST_EVENT_FILE="+eventFile,
			"CPAMP_TEST_SIGTERM_DELAY_MS=25",
		),
		Timings: fastTimings(),
		Stdout:  &bytes.Buffer{},
		Stderr:  &bytes.Buffer{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunSupervisor(ctx, cfg)
	}()

	waitForEvents(t, eventFile, 3, func(events []recordedEvent) bool {
		return len(events) >= 3
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunSupervisor failed: %v", err)
	}

	evs := parseEvents(t, eventFile)
	var stopSeq []string
	for _, e := range evs {
		if e.Kind == "sigterm" || e.Kind == "exit" {
			stopSeq = append(stopSeq, e.Kind+":"+e.Role)
		}
	}
	wantSeq := []string{
		"sigterm:ingress",
		"exit:ingress",
		"sigterm:manager",
		"exit:manager",
		"sigterm:supervisor",
		"exit:supervisor",
	}
	if strings.Join(stopSeq, ",") != strings.Join(wantSeq, ",") {
		t.Fatalf("drain sequence = %v, want %v", stopSeq, wantSeq)
	}
}

func TestDrainTimeoutEscalatesToSIGKILL(t *testing.T) {
	eventFile := filepath.Join(t.TempDir(), "events.log")
	timings := fastTimings()
	timings.DrainTimeout = 50 * time.Millisecond
	timings.IngressDrainGraceExtra = 30 * time.Millisecond

	cfg := SupervisorConfig{
		SupervisorSpec: helperChildSpec(t, "supervisor", "CPAMP_TEST_EVENT_FILE="+eventFile),
		ManagerSpec:    helperChildSpec(t, "manager", "CPAMP_TEST_EVENT_FILE="+eventFile),
		IngressSpec: helperChildSpec(t, "ingress",
			"CPAMP_TEST_EVENT_FILE="+eventFile,
			"CPAMP_TEST_IGNORE_SIGTERM=1",
		),
		Timings: timings,
		Stdout:  &bytes.Buffer{},
		Stderr:  &bytes.Buffer{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunSupervisor(ctx, cfg)
	}()

	evs := waitForEvents(t, eventFile, 3, nil)
	var ingressPID int
	for _, e := range evs {
		if e.Role == "ingress" && e.Kind == "start" {
			ingressPID = e.PID
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunSupervisor failed: %v", err)
	}
	if err := syscall.Kill(ingressPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("expected stubborn ingress pid %d to be killed and reaped, got err=%v", ingressPID, err)
	}
}

func TestSupervisorExitKillsProcessGroup(t *testing.T) {
	eventFile := filepath.Join(t.TempDir(), "events.log")
	subPIDFile := filepath.Join(t.TempDir(), "subchild.pid")
	seqFile := filepath.Join(t.TempDir(), "sup-seq.txt")

	cfg := SupervisorConfig{
		SupervisorSpec: helperChildSpec(t, "supervisor",
			"CPAMP_TEST_EVENT_FILE="+eventFile,
			"CPAMP_TEST_SPAWN_SUBCHILD_PID_FILE="+subPIDFile,
			"CPAMP_TEST_CRASH_SEQUENCE_FILE="+seqFile,
			"CPAMP_TEST_CRASH_DURATIONS_MS=60,0",
		),
		ManagerSpec: helperChildSpec(t, "manager", "CPAMP_TEST_EVENT_FILE="+eventFile),
		IngressSpec: helperChildSpec(t, "ingress", "CPAMP_TEST_EVENT_FILE="+eventFile),
		Timings:     fastTimings(),
		Stdout:      &bytes.Buffer{},
		Stderr:      &bytes.Buffer{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunSupervisor(ctx, cfg)
	}()

	var firstSubPID int
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(subPIDFile); err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil && pid > 0 {
				firstSubPID = pid
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if firstSubPID == 0 {
		cancel()
		t.Fatalf("subchild PID file was not written")
	}

	// Wait for supervisor to crash once and restart (attempt 2).
	waitForEvents(t, eventFile, 4, func(events []recordedEvent) bool {
		supStarts := 0
		for _, e := range events {
			if e.Role == "supervisor" && e.Kind == "start" {
				supStarts++
			}
		}
		return supStarts >= 2
	})

	if err := syscall.Kill(firstSubPID, 0); !errors.Is(err, syscall.ESRCH) {
		cancel()
		t.Fatalf("expected residual subchild pid %d in supervisor process group to be killed/reaped, got %v", firstSubPID, err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunSupervisor failed: %v", err)
	}
}

func TestUnrecoverableSupervisorCrashLoopExitsNonZero(t *testing.T) {
	eventFile := filepath.Join(t.TempDir(), "events.log")
	timings := fastTimings()
	timings.InitialBackoff = 10 * time.Millisecond
	timings.MaxBackoff = 20 * time.Millisecond
	timings.SupervisorShortRunWindow = 500 * time.Millisecond
	timings.SupervisorMaxConsecutiveShortFailures = 5

	cfg := SupervisorConfig{
		SupervisorSpec: helperChildSpec(t, "supervisor",
			"CPAMP_TEST_EVENT_FILE="+eventFile,
			"CPAMP_TEST_EXIT_AFTER_MS=10",
		),
		ManagerSpec: helperChildSpec(t, "manager", "CPAMP_TEST_EVENT_FILE="+eventFile),
		IngressSpec: helperChildSpec(t, "ingress", "CPAMP_TEST_EVENT_FILE="+eventFile),
		Timings:     timings,
		Stdout:      &bytes.Buffer{},
		Stderr:      &bytes.Buffer{},
	}

	err := RunSupervisor(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "supervisor failed 5 consecutive starts") {
		t.Fatalf("expected fatal supervisor crash-loop error, got: %v", err)
	}
}

func TestManagerBootstrapDiscoveryModeReadinessGating(t *testing.T) {
	eventFile := filepath.Join(t.TempDir(), "events.log")
	mgrAddr := reserveLoopbackAddr(t)

	timings := fastTimings()
	timings.ReadinessWaitTimeout = 2 * time.Second

	var errBuf bytes.Buffer
	cfg := SupervisorConfig{
		SupervisorSpec: helperChildSpec(t, "supervisor", "CPAMP_TEST_EVENT_FILE="+eventFile),
		ManagerSpec: helperChildSpec(t, "manager",
			"CPAMP_TEST_EVENT_FILE="+eventFile,
			"CPAMP_TEST_HTTP_LISTEN_ADDR="+mgrAddr,
			"CPAMP_TEST_HTTP_BOOTSTRAP_ONLY=1",
		),
		IngressSpec:      helperChildSpec(t, "ingress", "CPAMP_TEST_EVENT_FILE="+eventFile),
		ManagerHealthURL: "http://" + mgrAddr + "/health",
		Timings:          timings,
		Stdout:           &bytes.Buffer{},
		Stderr:           &errBuf,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunSupervisor(ctx, cfg)
	}()

	waitForEvents(t, eventFile, 3, func(events []recordedEvent) bool {
		starts := 0
		for _, e := range events {
			if e.Kind == "start" {
				starts++
			}
		}
		return starts >= 3
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunSupervisor failed: %v", err)
	}
	if strings.Contains(errBuf.String(), "manager readiness wait timed out") {
		t.Fatalf("expected bootstrap discovery mode to satisfy readiness immediately, got stderr:\n%s", errBuf.String())
	}
}
