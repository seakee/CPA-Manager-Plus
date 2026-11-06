package cpaprocess

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestObserveBeforeStart(t *testing.T) {
	var manager Manager
	if got := manager.Observe(); got != (Observation{State: StateNotStarted}) {
		t.Fatalf("Observe() = %+v", got)
	}
}

func TestStartObserveAndReap(t *testing.T) {
	for _, code := range []int{0, 23} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			t.Parallel()
			var manager Manager
			child := newHelper(t, &manager, code)
			started := startHelper(t, &manager, child)
			waitFor(t, func() bool { return len(child.starts(t)) == 1 })
			manager.mu.Lock()
			cmd := manager.cmd
			manager.mu.Unlock()

			got, err := manager.Start(t.Context(), child.spec)
			if !errors.Is(err, ErrStateConflict) || got != started {
				t.Fatalf("duplicate Start() = %+v, %v", got, err)
			}
			child.release(t)
			assertExit(t, waitForExit(t, &manager), code)
			if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
				t.Fatal("exit was published before Cmd.Wait reaped the child")
			}
		})
	}
}

func TestSpawnFailureAllowsRetry(t *testing.T) {
	t.Parallel()
	var manager Manager
	missing := StartSpec{Executable: filepath.Join(t.TempDir(), "missing-cpa")}
	got, err := manager.Start(t.Context(), missing)
	if !errors.Is(err, ErrSpawnFailed) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing executable Start() error = %v", err)
	}
	if got != (Observation{State: StateNotStarted}) || manager.Observe() != got {
		t.Fatalf("failed spawn left child state: %+v", got)
	}
	child := newHelper(t, &manager, 0)
	startHelper(t, &manager, child)
	child.release(t)
	exited := waitForExit(t, &manager)
	assertExit(t, exited, 0)
	got, err = manager.Start(t.Context(), missing)
	if !errors.Is(err, ErrSpawnFailed) || got != exited || manager.Observe() != exited {
		t.Fatalf("failed retry changed the last exit: %+v, %v", got, err)
	}
}

func TestInvalidStartSpec(t *testing.T) {
	for _, spec := range []StartSpec{
		{},
		{Executable: " \t"},
		{Executable: "cpa\x00"},
		{Executable: "cpa", Args: []string{"secret\x00argument"}},
	} {
		var manager Manager
		got, err := manager.Start(t.Context(), spec)
		if !errors.Is(err, ErrInvalidSpec) || got != (Observation{State: StateNotStarted}) {
			t.Fatalf("invalid Start() = %+v, %v", got, err)
		}
	}
}

func TestPreSpawnValidationFailurePerformsNoOSSpawnOrOwnershipPublication(t *testing.T) {
	manager := NewManager(func(StartSpec) error { return errors.New("staged bytes changed") })
	observation, err := manager.Start(t.Context(), StartSpec{Executable: "unused-test-command"})
	if !errors.Is(err, ErrSpawnFailed) || observation.State != StateNotStarted || manager.Observe().State != StateNotStarted {
		t.Fatalf("Start() = %+v, %v; observed=%+v", observation, err, manager.Observe())
	}
}

func TestConfiguredIdentityFailurePerformsNoOSSpawnOrRootFallback(t *testing.T) {
	manager, err := NewManagerWithIdentity(nil, ChildIdentity{UID: 10001, GID: 10001})
	if err != nil {
		t.Fatal(err)
	}
	manager.applyIdentity = func(*exec.Cmd, ChildIdentity) error {
		return errors.New("credential setup rejected")
	}
	child := newHelper(t, manager, 0)
	observation, err := manager.Start(t.Context(), child.spec)
	if !errors.Is(err, ErrSpawnFailed) || !strings.Contains(err.Error(), "credential setup rejected") ||
		observation.State != StateNotStarted || manager.Observe().State != StateNotStarted {
		t.Fatalf("Start() = %+v, %v; observed=%+v", observation, err, manager.Observe())
	}
	if starts := child.starts(t); len(starts) != 0 {
		t.Fatalf("credential failure spawned a fallback child: %v", starts)
	}
}

func TestConfiguredIdentityRejectsRootAndDefaultManagerRemainsUnconfigured(t *testing.T) {
	for _, identity := range []ChildIdentity{{UID: 0, GID: 10001}, {UID: 10001, GID: 0}} {
		if manager, err := NewManagerWithIdentity(nil, identity); !errors.Is(err, ErrInvalidIdentity) || manager != nil {
			t.Fatalf("NewManagerWithIdentity(%+v) = %v, %v", identity, manager, err)
		}
	}
	if manager := NewManager(nil); manager.childIdentity != nil {
		t.Fatalf("default Manager configured child identity = %+v", manager.childIdentity)
	}
}

func TestBeforeSpawnRunsOnlyAtEligibleSpawnBoundary(t *testing.T) {
	var observations int
	manager := NewManager(func(StartSpec) error { observations++; return nil })
	if _, err := manager.Start(t.Context(), StartSpec{}); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("invalid Start() error = %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := manager.Start(canceled, StartSpec{Executable: "unused"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Start() error = %v", err)
	}
	if observations != 0 {
		t.Fatalf("rejected Starts observed %d spawn boundaries", observations)
	}

	first := newHelper(t, manager, 0)
	started := startHelper(t, manager, first)
	if observations != 1 {
		t.Fatalf("first spawn observations = %d, want 1", observations)
	}
	if _, err := manager.Start(t.Context(), first.spec); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("duplicate Start() error = %v", err)
	}
	if observations != 1 {
		t.Fatalf("conflicting Start observed %d spawn boundaries", observations)
	}
	first.release(t)
	if got := waitForExit(t, manager); got.InstanceID != started.InstanceID {
		t.Fatalf("first exit = %+v, want instance %d", got, started.InstanceID)
	}

	second := newHelper(t, manager, 0)
	startHelper(t, manager, second)
	if observations != 2 {
		t.Fatalf("replacement spawn observations = %d, want 2", observations)
	}
	second.release(t)
	assertExit(t, waitForExit(t, manager), 0)
}

func TestConcurrentStartSpawnsExactlyOneChild(t *testing.T) {
	t.Parallel()
	var manager Manager
	child := newHelper(t, &manager, 0)
	const attempts = 32
	gate := make(chan struct{})
	results := make(chan error, attempts)
	var callers sync.WaitGroup
	for range attempts {
		callers.Add(1)
		go func() {
			defer callers.Done()
			<-gate
			_, err := manager.Start(t.Context(), child.spec)
			results <- err
			_ = manager.Observe()
		}()
	}
	close(gate)
	callers.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrStateConflict):
			conflicts++
		default:
			t.Fatalf("concurrent Start() error = %v", err)
		}
	}
	if successes != 1 || conflicts != attempts-1 {
		t.Fatalf("successes = %d, conflicts = %d", successes, conflicts)
	}
	waitFor(t, func() bool { return len(child.starts(t)) > 0 })
	entries := child.starts(t)
	if len(entries) != 1 || entries[0].Name() != strconv.Itoa(manager.Observe().PID) {
		t.Fatalf("spawned child markers = %v, observation = %+v", entries, manager.Observe())
	}
	child.release(t)
	assertExit(t, waitForExit(t, &manager), 0)
	if entries := child.starts(t); len(entries) != 1 {
		t.Fatalf("spawned %d children", len(entries))
	}
}

func TestCanceledContextDoesNotSpawn(t *testing.T) {
	t.Parallel()
	var manager Manager
	child := newHelper(t, &manager, 0)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, err := manager.Start(ctx, child.spec)
	if !errors.Is(err, context.Canceled) || got != (Observation{State: StateNotStarted}) {
		t.Fatalf("canceled Start() = %+v, %v", got, err)
	}
	if manager.Observe() != got || len(child.starts(t)) != 0 {
		t.Fatal("canceled Start created a child")
	}
	startHelper(t, &manager, child)
	child.release(t)
	assertExit(t, waitForExit(t, &manager), 0)
}

func TestCancelAfterStartDoesNotKillChild(t *testing.T) {
	t.Parallel()
	var manager Manager
	child := newHelper(t, &manager, 0)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started, err := manager.Start(ctx, child.spec)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(child.starts(t)) == 1 })
	cancel()
	for deadline := time.Now().Add(100 * time.Millisecond); time.Now().Before(deadline); {
		if got := manager.Observe(); got != started {
			t.Fatalf("caller cancellation changed the running child: %+v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	child.release(t)
	assertExit(t, waitForExit(t, &manager), 0)
}

func TestFastExitAllowsAnotherStart(t *testing.T) {
	t.Parallel()
	var manager Manager
	first := newHelper(t, &manager, 23)
	first.release(t) // The helper can exit as soon as the OS schedules it.
	startHelper(t, &manager, first)
	assertExit(t, waitForExit(t, &manager), 23)
	second := newHelper(t, &manager, 0)
	startHelper(t, &manager, second)
	second.release(t)
	assertExit(t, waitForExit(t, &manager), 0)
}

func TestArgumentsRemainLiteral(t *testing.T) {
	t.Parallel()
	var manager Manager
	child := newHelper(t, &manager, 0)
	want := []string{"value with spaces", "$(not-a-command)", ";", "*", ""}
	child.spec.Args = append(child.spec.Args, want...)
	started := startHelper(t, &manager, child)
	waitFor(t, func() bool { return len(child.starts(t)) == 1 })
	child.release(t)
	assertExit(t, waitForExit(t, &manager), 0)
	if got := child.report(t, started.PID).Args; !reflect.DeepEqual(got, want) {
		t.Fatalf("child arguments = %q, want %q", got, want)
	}
}

func TestStartFiltersOnlySupervisorPrivateEnvironment(t *testing.T) {
	var manager Manager
	child := newHelper(t, &manager, 0)
	private := []string{
		"CPAMP_RUNTIME_TOKEN", "CPAMP_RUNTIME_JOURNAL_PATH", "CPAMP_RUNTIME_IDENTITY",
		"CPAMP_RUNTIME_ADDR", "CPAMP_RUNTIME_GENERATION", "CPAMP_CPA_EXECUTABLE",
		"CPAMP_CPA_ARTIFACT_MANIFEST", "CPAMP_RUNTIME_FUTURE_SECRET", "CPAMP_RUNTIME_CPA_ADDR",
		"CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK", "MANAGEMENT_PASSWORD",
	}
	for _, key := range private {
		t.Setenv(key, "test-only-private-value")
	}
	inherited := []string{
		"PATH", "TMP", "TEMP", "HOME", "TMPDIR", "USERPROFILE",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR",
		"TZ", "LANG", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "NORMAL_SENTINEL",
		"CPAMP_DEPLOYMENT_SENTINEL", "CPAMP_CPA_EXECUTABLE_SUFFIX", "CPAMP_RUNTIME",
	}
	for _, key := range inherited {
		t.Setenv(key, child.dir)
	}
	started := startHelper(t, &manager, child)
	waitFor(t, func() bool { return len(child.starts(t)) == 1 })
	child.release(t)
	assertExit(t, waitForExit(t, &manager), 0)
	report := child.report(t, started.PID)
	for _, key := range private {
		if _, found := report.Environment[key]; found {
			t.Errorf("CPA child inherited private environment variable %s", key)
		}
	}
	for _, key := range inherited {
		if !report.Environment[key] {
			t.Errorf("CPA child did not preserve ordinary environment variable %s", key)
		}
	}
}

const (
	helperHTTPProxy  = "http://http-proxy.invalid:18080"
	helperHTTPSProxy = "http://https-proxy.invalid:18443"
	helperNoProxy    = "bypass.invalid"
)

func TestStartPreservesProxyEnvironment(t *testing.T) {
	for _, casing := range []string{"uppercase", "lowercase"} {
		t.Run(casing, func(t *testing.T) {
			var manager Manager
			child := newHelper(t, &manager, 0)
			// ProxyFromEnvironment caches its first environment read, so inspect
			// routing in a fresh child. No network connection is made.
			for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "REQUEST_METHOD"} {
				t.Setenv(key, "")
			}
			for key, value := range map[string]string{
				"HTTP_PROXY": helperHTTPProxy, "HTTPS_PROXY": helperHTTPSProxy, "NO_PROXY": helperNoProxy,
			} {
				if casing == "lowercase" {
					key = strings.ToLower(key)
				}
				t.Setenv(key, value)
			}
			child.spec.Args = append(child.spec.Args, "check-proxy-environment")
			started := startHelper(t, &manager, child)
			waitFor(t, func() bool { return len(child.starts(t)) == 1 })
			child.release(t)
			assertExit(t, waitForExit(t, &manager), 0)
			want := map[string]bool{"http": true, "https": true, "http bypass": true, "https bypass": true}
			if got := child.report(t, started.PID).ProxyRouting; !reflect.DeepEqual(got, want) {
				t.Fatalf("child proxy routing checks = %v, want %v", got, want)
			}
		})
	}
}

func TestChildEnvironmentFiltersSupervisorPrivateVariables(t *testing.T) {
	tests := []struct {
		name    string
		windows bool
		parent  []string
		want    []string
	}{
		{
			name: "Unix names are case sensitive",
			parent: []string{
				"CPAMP_RUNTIME_TOKEN=secret", "CPAMP_RUNTIME_FUTURE_SECRET=secret", "CPAMP_CPA_EXECUTABLE=/cpa",
				"CPAMP_CPA_ARTIFACT_MANIFEST=/trusted/artifact.json",
				"CPAMP_RUNTIME_CPA_ADDR=127.0.0.1:8317",
				"CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK=1", "MANAGEMENT_PASSWORD=secret",
				"cpamp_runtime_token=ordinary", "cpamp_cpa_executable=ordinary", "CPAMP_RUNTIME=ordinary",
				"CPAMP_DEPLOYMENT_SENTINEL=ordinary", "CPAMP_CPA_EXECUTABLE_SUFFIX=ordinary",
				"NORMAL_SENTINEL=first", "CPAMP_RUNTIME_TOKEN=duplicate-secret", "NORMAL_SENTINEL=",
				"PATH=/bin", "HOME=/home/cpa", "HTTP_PROXY=http://proxy.invalid:3128", "TEMP= /tmp/with=equals ",
			},
			want: []string{
				"cpamp_runtime_token=ordinary", "cpamp_cpa_executable=ordinary", "CPAMP_RUNTIME=ordinary",
				"CPAMP_DEPLOYMENT_SENTINEL=ordinary", "CPAMP_CPA_EXECUTABLE_SUFFIX=ordinary",
				"NORMAL_SENTINEL=first", "NORMAL_SENTINEL=",
				"PATH=/bin", "HOME=/home/cpa", "HTTP_PROXY=http://proxy.invalid:3128", "TEMP= /tmp/with=equals ",
			},
		},
		{
			name:    "Windows names are case insensitive",
			windows: true,
			parent: []string{
				`Path=C:\bin`, `SystemRoot=C:\Windows`, `wInDiR=C:\Windows`, `UserProfile=C:\Users\cpa`,
				`HomeDrive=C:`, `HomePath=\Users\cpa`, `tMp=C:\tmp`, `TeMp=C:\temp`,
				"CpAmP_RuNtImE_ToKeN=secret", "cpamp_runtime_future_secret=secret", "cpamp_cpa_executable=cpa.exe",
				"CpAmP_RuNtImE_CpA_AdDr=127.0.0.1:8317",
				"CpAmP_RuNtImE_CpA_LiStEn_LoOpBaCk=1", "MaNaGeMeNt_PaSsWoRd=secret",
				"Cpamp_Deployment_Sentinel=ordinary", "Cpamp_Cpa_Executable_Suffix=ordinary", "CPAMP_RUNTIME=ordinary",
				"Http_Proxy=http://proxy.invalid:3128", `=C:=C:\work`,
			},
			want: []string{
				`Path=C:\bin`, `SystemRoot=C:\Windows`, `wInDiR=C:\Windows`, `UserProfile=C:\Users\cpa`,
				`HomeDrive=C:`, `HomePath=\Users\cpa`, `tMp=C:\tmp`, `TeMp=C:\temp`,
				"Cpamp_Deployment_Sentinel=ordinary", "Cpamp_Cpa_Executable_Suffix=ordinary", "CPAMP_RUNTIME=ordinary",
				"Http_Proxy=http://proxy.invalid:3128", `=C:=C:\work`,
			},
		},
		{name: "empty Unix environment", want: []string{}},
		{name: "empty Windows environment", windows: true, want: []string{}},
		{name: "only private Unix variables", parent: []string{"CPAMP_RUNTIME_TOKEN=secret"}, want: []string{}},
		{name: "only private Windows variables", windows: true, parent: []string{"cpamp_runtime_token=secret"}, want: []string{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := slices.Clone(test.parent)
			got := childEnvironment(test.parent, test.windows)
			if got == nil {
				t.Fatal("nil child environment would restore full Supervisor inheritance")
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("child environment = %q, want %q", got, test.want)
			}
			if !reflect.DeepEqual(test.parent, parent) {
				t.Fatal("filter changed the parent environment slice")
			}
		})
	}
}

func TestChildEnvironmentStripsManagementPassword(t *testing.T) {
	parent := []string{
		"FIRST_SENTINEL=alpha",
		"MANAGEMENT_PASSWORD=operator-exported-password",
		"CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK=1",
		"MIDDLE_SENTINEL=beta=gamma",
		"MANAGEMENT_PASSWORD=",
		"LAST_SENTINEL=omega",
	}
	want := []string{
		"FIRST_SENTINEL=alpha",
		"MIDDLE_SENTINEL=beta=gamma",
		"LAST_SENTINEL=omega",
	}
	for _, windows := range []bool{false, true} {
		if got := childEnvironment(parent, windows); !reflect.DeepEqual(got, want) {
			t.Fatalf("childEnvironment(windows=%v) = %q, want %q", windows, got, want)
		}
	}
}

func TestSignalExitHasNoInventedExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows Kill reports an exit code rather than Unix signal termination")
	}
	t.Parallel()
	var manager Manager
	child := newHelper(t, &manager, 0)
	started := startHelper(t, &manager, child)
	waitFor(t, func() bool { return len(child.starts(t)) == 1 })
	manager.mu.Lock()
	process := manager.cmd.Process
	manager.mu.Unlock()
	// Only the test owns this cleanup action; the product has no Kill/Stop API.
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	if got := waitForExit(t, &manager); got != (Observation{State: StateExited, InstanceID: started.InstanceID}) {
		t.Fatalf("signal exit = %+v, want unknown exit code", got)
	}
}

func TestStopTerminatesExactOwnedChildAfterConfirmedReap(t *testing.T) {
	t.Parallel()
	var manager Manager
	child := newHelper(t, &manager, 0)
	startHelper(t, &manager, child)
	waitFor(t, func() bool { return len(child.starts(t)) == 1 })
	manager.mu.Lock()
	cmd := manager.cmd
	manager.mu.Unlock()
	target, err := manager.PrepareStop()
	if err != nil {
		t.Fatal(err)
	}
	got, err := target.Terminate(t.Context())
	if err != nil || got.State != StateExited || got.PID != 0 || got.WaitError != nil {
		t.Fatalf("Terminate() = %+v, %v", got, err)
	}
	if cmd.ProcessState == nil {
		t.Fatal("Stop succeeded before Cmd.Wait reaped the exact child")
	}

	next := newHelper(t, &manager, 0)
	startHelper(t, &manager, next)
	next.release(t)
	assertExit(t, waitForExit(t, &manager), 0)
}

func TestStopTargetPreventsABAKillOfReplacementChild(t *testing.T) {
	t.Parallel()
	var manager Manager
	first := newHelper(t, &manager, 0)
	startHelper(t, &manager, first)
	waitFor(t, func() bool { return len(first.starts(t)) == 1 })
	target, err := manager.PrepareStop()
	if err != nil {
		t.Fatal(err)
	}

	// The target exits naturally after it is reserved. Its Wait path may clear
	// process ownership, but Start remains fenced until this exact target is
	// resolved or released.
	first.release(t)
	assertExit(t, waitForExit(t, &manager), 0)
	second := newHelper(t, &manager, 0)
	if _, err := manager.Start(t.Context(), second.spec); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("replacement Start while old Stop target is reserved = %v", err)
	}
	if got, err := target.Terminate(t.Context()); err != nil || got.State != StateExited {
		t.Fatalf("natural-exit Stop = %+v, %v", got, err)
	}

	secondStarted := startHelper(t, &manager, second)
	waitFor(t, func() bool { return len(second.starts(t)) == 1 })
	if _, err := target.Terminate(t.Context()); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("old target reuse = %v", err)
	}
	if got := manager.Observe(); got != secondStarted {
		t.Fatalf("old Stop target changed replacement child: %+v, want %+v", got, secondStarted)
	}
	second.release(t)
	assertExit(t, waitForExit(t, &manager), 0)
}

func TestPrepareStopFailsClosedWithoutConfirmedRunningOwnership(t *testing.T) {
	for _, test := range []struct {
		name  string
		state State
		owned bool
	}{
		{name: "not started", state: StateNotStarted},
		{name: "exited", state: StateExited},
		{name: "unknown", state: StateUnknown, owned: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := Manager{observation: Observation{State: test.state}}
			if test.owned {
				manager.cmd = exec.Command("unused")
				manager.waitDone = make(chan struct{})
				manager.observation.PID = 123
			}
			if target, err := manager.PrepareStop(); target != nil || !errors.Is(err, ErrStateConflict) {
				t.Fatalf("PrepareStop() = %#v, %v", target, err)
			}
		})
	}
}

func TestOnlyOneStopTargetCanReserveOwnedChild(t *testing.T) {
	t.Parallel()
	var manager Manager
	child := newHelper(t, &manager, 0)
	startHelper(t, &manager, child)
	waitFor(t, func() bool { return len(child.starts(t)) == 1 })
	target, err := manager.PrepareStop()
	if err != nil {
		t.Fatal(err)
	}
	if duplicate, err := manager.PrepareStop(); duplicate != nil || !errors.Is(err, ErrStateConflict) {
		t.Fatalf("duplicate PrepareStop() = %#v, %v", duplicate, err)
	}
	target.Release()
	retry, err := manager.PrepareStop()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retry.Terminate(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestWaitFailureRetainsOwnership(t *testing.T) {
	// Fault-inject a Wait error with no ProcessState using an unstarted Cmd.
	// No real child is created or abandoned by this error-path test.
	cmd := exec.Command("unused-test-command")
	done := make(chan struct{})
	manager := Manager{cmd: cmd, waitDone: done, observation: Observation{State: StateRunning, InstanceID: 1, PID: 123}}
	events := manager.ExitEvents()
	manager.wait(cmd, done)
	got := manager.Observe()
	if got.State != StateUnknown || got.PID != 123 || got.WaitError == nil || got.ExitCodeKnown {
		t.Fatalf("unconfirmed wait = %+v", got)
	}
	if manager.cmd != cmd {
		t.Fatal("unconfirmed wait released child ownership")
	}
	if _, err := manager.Start(t.Context(), StartSpec{Executable: "unused-test-command"}); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("Start after unconfirmed wait error = %v", err)
	}
	select {
	case event := <-events:
		t.Fatalf("unconfirmed Wait published recoverable exit: %+v", event)
	default:
	}
}

type helperChild struct {
	spec StartSpec
	dir  string
}

type helperReport struct {
	Args []string
	// Record names and whether the value equals the fixture directory, never
	// arbitrary environment values that could contain Supervisor credentials.
	Environment  map[string]bool
	ProxyRouting map[string]bool
}

func newHelper(t *testing.T, manager *Manager, code int) helperChild {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "starts"), 0o700); err != nil {
		t.Fatal(err)
	}
	child := helperChild{
		dir: dir,
		spec: StartSpec{Executable: executable, Args: []string{
			"-test.run=^TestCPAProcessHelper$", "--", "cpamp-cpa-test-child", dir, strconv.Itoa(code),
		}},
	}
	t.Cleanup(func() {
		child.release(t)
		waitFor(t, func() bool { return manager.Observe().State != StateRunning })
	})
	return child
}

func startHelper(t *testing.T, manager *Manager, child helperChild) Observation {
	t.Helper()
	got, err := manager.Start(t.Context(), child.spec)
	if err != nil || got.State != StateRunning || got.PID <= 0 || got.ExitCodeKnown || got.WaitError != nil {
		t.Fatalf("Start() = %+v, %v", got, err)
	}
	return got
}

func (child helperChild) starts(t *testing.T) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(child.dir, "starts"))
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func (child helperChild) report(t *testing.T, pid int) helperReport {
	t.Helper()
	encoded, err := os.ReadFile(filepath.Join(child.dir, "starts", strconv.Itoa(pid)))
	if err != nil {
		t.Fatal(err)
	}
	var report helperReport
	if err := json.Unmarshal(encoded, &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func (child helperChild) release(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(child.dir, "exit"), nil, 0o600); err != nil {
		t.Error(err)
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for helper process observation")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForExit(t *testing.T, manager *Manager) Observation {
	t.Helper()
	waitFor(t, func() bool { return manager.Observe().State == StateExited })
	return manager.Observe()
}

func assertExit(t *testing.T, got Observation, code int) {
	t.Helper()
	if got.InstanceID == 0 || got != (Observation{State: StateExited, InstanceID: got.InstanceID, ExitCode: code, ExitCodeKnown: true}) {
		t.Fatalf("exit = %+v, want reaped child with code %d", got, code)
	}
}

func TestCPAProcessHelper(t *testing.T) {
	separator := slices.Index(os.Args, "--")
	if separator < 0 || len(os.Args) < separator+4 || os.Args[separator+1] != "cpamp-cpa-test-child" {
		return
	}
	dir := os.Args[separator+2]
	code, err := strconv.Atoi(os.Args[separator+3])
	if err != nil {
		os.Exit(96)
	}
	report := helperReport{Args: os.Args[separator+4:], Environment: make(map[string]bool)}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		report.Environment[key] = value == dir
	}
	if slices.Contains(report.Args, "check-proxy-environment") {
		report.ProxyRouting = make(map[string]bool)
		for _, test := range []struct{ name, target, want string }{
			{"http", "http://destination.invalid/", helperHTTPProxy},
			{"https", "https://destination.invalid/", helperHTTPSProxy},
			{"http bypass", "http://" + helperNoProxy + "/", ""},
			{"https bypass", "https://" + helperNoProxy + "/", ""},
		} {
			request, err := http.NewRequest(http.MethodGet, test.target, nil)
			if err != nil {
				os.Exit(96)
			}
			proxyURL, err := http.ProxyFromEnvironment(request)
			var got string
			if proxyURL != nil {
				got = proxyURL.String()
			}
			// Report only equality with synthetic fixture values, never parent credentials.
			report.ProxyRouting[test.name] = err == nil && got == test.want
		}
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		os.Exit(96)
	}
	if err := os.WriteFile(filepath.Join(dir, "starts", strconv.Itoa(os.Getpid())), encoded, 0o600); err != nil {
		os.Exit(96)
	}
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(filepath.Join(dir, "exit")); err == nil {
			os.Exit(code)
		}
		time.Sleep(5 * time.Millisecond)
	}
	os.Exit(97)
}
