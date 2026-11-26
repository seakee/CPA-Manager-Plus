//go:build unix

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ChildSpec defines how a supervised process is launched.
type ChildSpec struct {
	Name       string
	Prefix     string
	Executable string
	Args       []string
	Dir        string
	Env        []string
	UID        int
	GID        int
	ClearSupp  bool
	HasUmask   bool
	Umask      int
}

// TimingConfig holds tunable supervision durations. Zero values fall back to
// the production package constants.
type TimingConfig struct {
	InitialBackoff                        time.Duration
	MaxBackoff                            time.Duration
	HealthyUptimeReset                    time.Duration
	SupervisorShortRunWindow              time.Duration
	SupervisorMaxConsecutiveShortFailures int
	DrainTimeout                          time.Duration
	IngressDrainGraceExtra                time.Duration
	ManagerShutdownTimeout                time.Duration
	SupervisorShutdownTimeout             time.Duration
	ProcessGroupKillGrace                 time.Duration
	SIGKILLReapWait                       time.Duration
	ChildIODrainTimeout                   time.Duration
	ReadinessPollInterval                 time.Duration
	UpgradeWorkerReadyTimeout             time.Duration
	UpgradeWorkerMaxInitialFailures       int
	TrialWatchdogTimeout                  time.Duration
	TrialStabilizationWindow              time.Duration
}

func (t TimingConfig) withDefaults(getenv func(string) string) TimingConfig {
	if t.InitialBackoff <= 0 {
		t.InitialBackoff = InitialRestartBackoff
	}
	if t.MaxBackoff <= 0 {
		t.MaxBackoff = MaxRestartBackoff
	}
	if t.HealthyUptimeReset <= 0 {
		t.HealthyUptimeReset = HealthyUptimeResetThreshold
	}
	if t.SupervisorShortRunWindow <= 0 {
		t.SupervisorShortRunWindow = SupervisorShortRunWindow
	}
	if t.SupervisorMaxConsecutiveShortFailures <= 0 {
		t.SupervisorMaxConsecutiveShortFailures = SupervisorMaxConsecutiveShortFailures
	}
	if t.DrainTimeout <= 0 {
		t.DrainTimeout = parseDrainTimeoutEnv(getenv)
	}
	if t.IngressDrainGraceExtra <= 0 {
		t.IngressDrainGraceExtra = IngressDrainGraceExtra
	}
	if t.ManagerShutdownTimeout <= 0 {
		t.ManagerShutdownTimeout = ManagerShutdownTimeout
	}
	if t.SupervisorShutdownTimeout <= 0 {
		t.SupervisorShutdownTimeout = SupervisorShutdownTimeout
	}
	if t.ProcessGroupKillGrace <= 0 {
		t.ProcessGroupKillGrace = ProcessGroupKillGrace
	}
	if t.SIGKILLReapWait <= 0 {
		t.SIGKILLReapWait = SIGKILLReapWait
	}
	if t.ChildIODrainTimeout <= 0 {
		t.ChildIODrainTimeout = ChildIODrainTimeout
	}
	if t.ReadinessPollInterval <= 0 {
		t.ReadinessPollInterval = DefaultReadinessPollInterval
	}
	if t.UpgradeWorkerReadyTimeout <= 0 {
		t.UpgradeWorkerReadyTimeout = UpgradeWorkerReadyTimeout
	}
	if t.UpgradeWorkerMaxInitialFailures <= 0 {
		t.UpgradeWorkerMaxInitialFailures = UpgradeWorkerMaxInitialFailures
	}
	if t.TrialWatchdogTimeout <= 0 {
		t.TrialWatchdogTimeout = DefaultTrialWatchdogTimeout
	}
	if t.TrialStabilizationWindow <= 0 {
		t.TrialStabilizationWindow = parseTrialStabilizationWindowEnv(getenv)
	}
	if t.TrialWatchdogTimeout > 0 && t.TrialWatchdogTimeout < 15*time.Second {
		maxSoak := min(DefaultTrialStabilizationWindow, t.TrialWatchdogTimeout/3)
		if maxSoak > 0 && t.TrialStabilizationWindow > maxSoak {
			t.TrialStabilizationWindow = maxSoak
		}
	}
	return t
}

func parseTrialStabilizationWindowEnv(getenv func(string) string) time.Duration {
	if getenv == nil {
		return DefaultTrialStabilizationWindow
	}
	raw := strings.TrimSpace(getenv("CPAMP_TRIAL_STABILIZATION_WINDOW"))
	if raw == "" {
		return DefaultTrialStabilizationWindow
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return DefaultTrialStabilizationWindow
	}
	return d
}

func parseDrainTimeoutEnv(getenv func(string) string) time.Duration {
	if getenv == nil {
		return DefaultDrainTimeout
	}
	raw := strings.TrimSpace(getenv("CPAMP_DRAIN_TIMEOUT"))
	if raw == "" {
		return DefaultDrainTimeout
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return DefaultDrainTimeout
	}
	return d
}

// SupervisorConfig configures the process supervisor for the three child services.
type SupervisorConfig struct {
	SupervisorSpec        ChildSpec
	ManagerSpec           ChildSpec
	UpgradeSpec           *ChildSpec
	UpgradeSocket         string
	IngressSpec           ChildSpec
	SupervisorTCPAddr     string
	SupervisorReadyFunc   func(ctx context.Context) bool
	ManagerHealthURL      string
	IngressPublicTCPAddr  string
	IngressAdminTCPAddr   string
	Timings               TimingConfig
	Stdout                io.Writer
	Stderr                io.Writer
	SetCredentials        bool
	BeforeSupervisorStart func(context.Context, ChildSpec) (ChildSpec, error)
	SlotControl           *SlotControlServer
	SlotStore             *SlotStore
	BaseInstallRoot       string
	BaseVersion           string
	ActiveSlot            string
	LauncherArgs          []string
	BaseEnviron           []string
	Trial                 *TrialWatchdogConfig
	OnChildStart          func(name string, pid int)
	OnAllReady            func()
	OnBeforeExec          func()
	beforeTrialCommit     func()
}

// NewSupervisorConfig builds the production SupervisorConfig from PreparedRuntime
// and the launcher's environment.
func NewSupervisorConfig(prepared *PreparedRuntime, environ []string) SupervisorConfig {
	envMap := parseEnvironMap(environ)
	getenv := func(k string) string { return envMap[k] }
	timings := (TimingConfig{}).withDefaults(getenv)

	return SupervisorConfig{
		SupervisorSpec: ChildSpec{
			Name:       "supervisor",
			Prefix:     "[supervisor] ",
			Executable: DefaultSupervisorExecutablePath,
			Dir:        prepared.GatewayDir,
			Env:        BuildSupervisorChildEnv(prepared, envMap),
			UID:        RootUID,
			GID:        RootGID,
			ClearSupp:  false,
		},
		ManagerSpec: ChildSpec{
			Name:       "manager",
			Prefix:     "[manager] ",
			Executable: DefaultManagerExecutablePath,
			Dir:        prepared.DataDir,
			Env:        BuildManagerChildEnv(prepared, envMap),
			UID:        prepared.ManagerUID,
			GID:        prepared.ManagerGID,
			ClearSupp:  true,
			HasUmask:   true,
			Umask:      0o077,
		},
		IngressSpec: ChildSpec{
			Name:       "ingress",
			Prefix:     "[ingress] ",
			Executable: DefaultIngressExecutablePath,
			Dir:        "/",
			Env:        BuildIngressChildEnv(timings.DrainTimeout, envMap),
			UID:        prepared.IngressUID,
			GID:        prepared.IngressGID,
			ClearSupp:  true,
		},
		SupervisorTCPAddr:     prepared.RuntimeAddr,
		ManagerHealthURL:      "http://" + DefaultManagerAddr + "/health",
		IngressPublicTCPAddr:  NormalizeTCPProbeAddr(DefaultIngressPublicAddr),
		IngressAdminTCPAddr:   DefaultIngressAdminAddr,
		Timings:               timings,
		Stdout:                os.Stdout,
		Stderr:                os.Stderr,
		SetCredentials:        os.Geteuid() == 0,
		BeforeSupervisorStart: supervisorPreparation(prepared, envMap, DefaultSystem()),
	}
}

func parseEnvironMap(environ []string) map[string]string {
	out := make(map[string]string, len(environ))
	for _, entry := range environ {
		k, v, ok := strings.Cut(entry, "=")
		if ok {
			out[k] = v
		}
	}
	return out
}

func baseScratchEnv(containerEnv map[string]string) map[string]string {
	pathVal := containerEnv["PATH"]
	if pathVal == "" {
		pathVal = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	env := map[string]string{
		"PATH": pathVal,
		"HOME": "/",
	}
	if tz := containerEnv["TZ"]; tz != "" {
		env["TZ"] = tz
	}
	return env
}

func formatEnvMap(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// BuildSupervisorChildEnv builds the clean environment for cpamp-runtime-supervisor.
func BuildSupervisorChildEnv(prepared *PreparedRuntime, containerEnv map[string]string) []string {
	env := baseScratchEnv(containerEnv)
	for _, kv := range prepared.SupervisorEnv() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	stripLauncherOnlyEnvVars(env)
	return formatEnvMap(env)
}

func stripLauncherOnlyEnvVars(env map[string]string) {
	for k := range env {
		if strings.HasPrefix(k, "CPAMP_LAUNCHER_") {
			delete(env, k)
		}
	}
	delete(env, "CPAMP_TRIAL_HOLD")
	delete(env, "CPAMP_ACTIVE_SLOT")
	delete(env, "CPAMP_BASE_INSTALL_ROOT")
	delete(env, "CPAMP_BASE_VERSION")
	delete(env, "CPAMP_RUNNING_SLOT")
}

// BuildManagerChildEnv builds the environment for cpa-manager-plus by passing
// through the container environment (which carries image ENV defaults and user
// overrides), stripping launcher/Supervisor-only secrets, and forcing the
// single-container loopback addresses and token file paths.
func BuildManagerChildEnv(prepared *PreparedRuntime, containerEnv map[string]string) []string {
	return BuildManagerChildEnvWithAddrs(
		prepared,
		DefaultManagerAddr,
		DefaultRuntimeAddr,
		DefaultIngressAdminAddr,
		containerEnv,
	)
}

// BuildManagerChildEnvWithAddrs builds Manager environment variables with explicit service addresses.
func BuildManagerChildEnvWithAddrs(
	prepared *PreparedRuntime,
	managerAddr string,
	runtimeAddr string,
	ingressControlAddr string,
	containerEnv map[string]string,
) []string {
	env := baseScratchEnv(containerEnv)
	for k, v := range containerEnv {
		if k != "" {
			env[k] = v
		}
	}

	tokenFile := prepared.TokenFile
	if tokenFile == "" {
		tokenFile = DefaultRuntimeTokenFile
	}
	launcherTokenFile := prepared.LauncherTokenFile
	if launcherTokenFile == "" {
		launcherTokenFile = filepath.Join(filepath.Dir(tokenFile), "launcher-token")
	}
	launcherControlAddr := prepared.LauncherControlAddr
	if launcherControlAddr == "" {
		launcherControlAddr = DefaultLauncherControlAddr
	}
	baseVersion := prepared.BaseVersion
	if baseVersion == "" {
		baseVersion = NormalizeVersionOrRaw(Version)
	}
	if managerAddr == "" {
		managerAddr = DefaultManagerAddr
	}
	if runtimeAddr == "" {
		runtimeAddr = prepared.RuntimeAddr
	}
	if runtimeAddr == "" {
		runtimeAddr = DefaultRuntimeAddr
	}
	if ingressControlAddr == "" {
		ingressControlAddr = DefaultIngressAdminAddr
	}

	env["HTTP_ADDR"] = managerAddr
	env["CPAMP_RUNTIME_URL"] = "http://" + runtimeAddr
	env["CPAMP_RUNTIME_TOKEN_FILE"] = tokenFile
	env["CPAMP_INGRESS_CONTROL_URL"] = "http://" + ingressControlAddr
	env["CPAMP_LAUNCHER_CONTROL_URL"] = "http://" + launcherControlAddr
	env["CPAMP_LAUNCHER_TOKEN_FILE"] = launcherTokenFile
	env["CPAMP_BASE_VERSION"] = baseVersion
	env["CPAMP_RUNNING_SLOT"] = prepared.RunningSlot
	if prepared.TrialHold {
		env["CPAMP_TRIAL_HOLD"] = "1"
	} else {
		delete(env, "CPAMP_TRIAL_HOLD")
	}

	baseInstallRoot := strings.TrimSpace(prepared.BaseInstallRoot)
	if baseInstallRoot != "" {
		env["CPAMP_BASE_INSTALL_ROOT"] = baseInstallRoot
	} else {
		delete(env, "CPAMP_BASE_INSTALL_ROOT")
	}

	delete(env, "CPAMP_RUNTIME_TOKEN")
	delete(env, "CPAMP_LAUNCHER_TOKEN")
	delete(env, "CPAMP_LAUNCHER_CONTROL_ADDR")
	delete(env, "CPAMP_ACTIVE_SLOT")
	return formatEnvMap(env)
}

// BuildIngressChildEnv builds the clean environment for cpamp-ingress.
// CPAMP_RUNTIME_TOKEN and CPAMP_LAUNCHER_* are never included.
func BuildIngressChildEnv(drainTimeout time.Duration, containerEnv map[string]string) []string {
	return BuildIngressChildEnvWithAddrs(
		drainTimeout,
		DefaultIngressPublicAddr,
		DefaultManagerAddr,
		DefaultRuntimeCPAAddr,
		DefaultIngressAdminAddr,
		containerEnv,
	)
}

// BuildIngressChildEnvWithAddrs builds Ingress environment variables with explicit addresses.
func BuildIngressChildEnvWithAddrs(
	drainTimeout time.Duration,
	ingressPublicAddr string,
	managerAddr string,
	gatewayAddr string,
	ingressControlAddr string,
	containerEnv map[string]string,
) []string {
	if drainTimeout <= 0 {
		drainTimeout = DefaultDrainTimeout
	}
	if ingressPublicAddr == "" {
		ingressPublicAddr = DefaultIngressPublicAddr
	}
	if managerAddr == "" {
		managerAddr = DefaultManagerAddr
	}
	if gatewayAddr == "" {
		gatewayAddr = DefaultRuntimeCPAAddr
	}
	if ingressControlAddr == "" {
		ingressControlAddr = DefaultIngressAdminAddr
	}
	env := baseScratchEnv(containerEnv)
	stripLauncherOnlyEnvVars(env)
	env["CPAMP_INGRESS_ADDR"] = ingressPublicAddr
	env["CPAMP_MANAGER_URL"] = "http://" + managerAddr
	env["CPAMP_GATEWAY_URL"] = "http://" + gatewayAddr
	env["CPAMP_INGRESS_CONTROL_ADDR"] = ingressControlAddr
	env["CPAMP_INGRESS_SHUTDOWN_TIMEOUT"] = drainTimeout.String()
	delete(env, "CPAMP_RUNTIME_TOKEN")
	return formatEnvMap(env)
}

type lineWriteReq struct {
	dst     io.Writer
	line    string
	flushCh chan struct{}
}

// lineSink decouples child and launcher log writes from potentially blocked
// stdout/stderr sinks so a stalled consumer cannot hold up child pipe draining
// or ordered shutdown.
type lineSink struct {
	ch       chan lineWriteReq
	stopCh   chan struct{}
	stopOnce sync.Once
}

func newLineSink() *lineSink {
	s := &lineSink{
		ch:     make(chan lineWriteReq, 4096),
		stopCh: make(chan struct{}),
	}
	go func() {
		for req := range s.ch {
			if req.flushCh != nil {
				close(req.flushCh)
				return
			}
			if req.dst != nil {
				_, _ = io.WriteString(req.dst, req.line)
			}
		}
	}()
	return s
}

func (s *lineSink) flushAndStop(timeout time.Duration) {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if timeout <= 0 {
			timeout = 50 * time.Millisecond
		}
		flushCh := make(chan struct{})
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case s.ch <- lineWriteReq{flushCh: flushCh}:
			select {
			case <-flushCh:
			case <-timer.C:
			}
		case <-timer.C:
		}
		close(s.stopCh)
	})
}

type syncLineWriter struct {
	sink *lineSink
	dst  io.Writer
}

func (w *syncLineWriter) writePrefixedLine(prefix, line string) {
	if w == nil || w.dst == nil || w.sink == nil {
		return
	}
	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	msg := prefix + line
	select {
	case <-w.sink.stopCh:
		return
	default:
	}
	select {
	case w.sink.ch <- lineWriteReq{dst: w.dst, line: msg}:
	case <-w.sink.stopCh:
	default:
	}
}

// Reaping design justification:
// A single central reaper goroutine owns all child and orphan reaping via
// syscall.Wait4(-1, &wstatus, syscall.WNOHANG, nil), triggered by SIGCHLD and a
// short periodic poll. We never call exec.Cmd.Wait(). Instead, child.Start()
// and registration of the child's PID in reaper.children[pid] happen while
// holding reaper.mu, and the reaper loop also holds reaper.mu around Wait4(-1).
// This guarantees Wait4(-1) can never reap a freshly spawned supervised child
// before its PID is registered, while any reparented orphan zombie (such as a
// residual grandchild) is automatically reaped without stealing supervised PIDs.
type centralReaper struct {
	mu       sync.Mutex
	children map[int]*runningChild
	sigchld  chan os.Signal
	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
}

func newCentralReaper() *centralReaper {
	sigchld := make(chan os.Signal, 8)
	signal.Notify(sigchld, syscall.SIGCHLD)
	r := &centralReaper{
		children: make(map[int]*runningChild),
		sigchld:  sigchld,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
	go r.loop()
	return r
}

func (r *centralReaper) stop() {
	r.stopOnce.Do(func() {
		signal.Stop(r.sigchld)
		close(r.stopCh)
		<-r.doneCh
	})
}

func (r *centralReaper) loop() {
	defer close(r.doneCh)
	ticker := time.NewTicker(15 * time.Millisecond)
	defer ticker.Stop()
	for {
		r.reapAvailable()
		select {
		case <-r.stopCh:
			r.reapAvailable()
			return
		case <-r.sigchld:
		case <-ticker.C:
		}
	}
}

func (r *centralReaper) reapAvailable() {
	for {
		r.mu.Lock()
		var status syscall.WaitStatus
		wpid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if wpid > 0 {
			reapedChild, ok := r.children[wpid]
			if ok {
				delete(r.children, wpid)
				reapedChild.waitStatus = status
				reapedChild.reaped = true
				close(reapedChild.exitCh)
			}
			r.mu.Unlock()
			if ok && reapedChild != nil && reapedChild.onReap != nil {
				reapedChild.onReap(status)
			}
			continue
		}
		r.mu.Unlock()
		if err == nil || errors.Is(err, syscall.ECHILD) || errors.Is(err, syscall.EINTR) {
			return
		}
		return
	}
}

type runningChild struct {
	spec           ChildSpec
	pid            int
	pgid           int
	startedAt      time.Time
	exitCh         chan struct{}
	waitStatus     syscall.WaitStatus
	reaped         bool
	onReap         func(syscall.WaitStatus)
	ioDone         chan struct{}
	closePipesOnce sync.Once
	outR           *os.File
	errR           *os.File
}

func (c *runningChild) closeOutputPipes() {
	if c == nil {
		return
	}
	c.closePipesOnce.Do(func() {
		for _, pipeReader := range []*os.File{c.outR, c.errR} {
			if pipeReader == nil {
				continue
			}
			_ = pipeReader.SetReadDeadline(time.Now())
			go func(f *os.File) {
				_ = f.Close()
			}(pipeReader)
		}
	})
}

func (r *centralReaper) startChild(
	spec ChildSpec,
	setCreds bool,
	stdoutWriter, stderrWriter *syncLineWriter,
	onReap func(syscall.WaitStatus),
) (*runningChild, error) {
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		_ = outR.Close()
		_ = outW.Close()
		return nil, err
	}

	cmd := exec.Command(spec.Executable, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.Stdout = outW
	cmd.Stderr = errW
	cmd.SysProcAttr = buildSysProcAttr(spec, setCreds)

	ioDone := make(chan struct{})
	child := &runningChild{
		spec:   spec,
		exitCh: make(chan struct{}),
		onReap: onReap,
		ioDone: ioDone,
		outR:   outR,
		errR:   errR,
	}

	r.mu.Lock()
	var oldUmask int
	if spec.HasUmask {
		oldUmask = syscall.Umask(spec.Umask)
	}
	startErr := cmd.Start()
	if spec.HasUmask {
		syscall.Umask(oldUmask)
	}
	if startErr == nil && cmd.Process != nil {
		pid := cmd.Process.Pid
		child.pid = pid
		child.pgid = pid
		child.startedAt = time.Now()
		r.children[pid] = child
		_ = cmd.Process.Release()
	}
	r.mu.Unlock()

	_ = outW.Close()
	_ = errW.Close()

	if startErr != nil {
		_ = outR.Close()
		_ = errR.Close()
		return nil, startErr
	}

	var ioWG sync.WaitGroup
	ioWG.Add(2)
	go func() {
		defer ioWG.Done()
		copyPrefixedLines(outR, stdoutWriter, spec.Prefix)
		_ = outR.Close()
	}()
	go func() {
		defer ioWG.Done()
		copyPrefixedLines(errR, stderrWriter, spec.Prefix)
		_ = errR.Close()
	}()
	go func() {
		ioWG.Wait()
		close(ioDone)
	}()

	return child, nil
}

func copyPrefixedLines(r io.Reader, dst *syncLineWriter, prefix string) {
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			dst.writePrefixedLine(prefix, line)
		}
		if err != nil {
			return
		}
	}
}

type managedService struct {
	spec         ChildSpec
	mu           sync.Mutex
	current      *runningChild
	stopped      bool
	starts       int
	reapedCount  int
	nonZeroExits int
	exitNotify   chan struct{}
	doneCh       chan struct{}
}

func newManagedService(spec ChildSpec) *managedService {
	return &managedService{
		spec:       spec,
		exitNotify: make(chan struct{}, 1),
		doneCh:     make(chan struct{}),
	}
}

func (s *managedService) notifyExit() {
	if s.exitNotify != nil {
		select {
		case s.exitNotify <- struct{}{}:
		default:
		}
	}
}

func (s *managedService) recordNonZeroExit() {
	s.mu.Lock()
	s.nonZeroExits++
	s.mu.Unlock()
	s.notifyExit()
}

func (s *managedService) startCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts
}

func (s *managedService) nonZeroFailureCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nonZeroExits
}

func (s *managedService) isCurrentlyRunning() bool {
	s.mu.Lock()
	cur := s.current
	reaped := s.reapedCount >= s.starts
	s.mu.Unlock()
	if cur == nil || reaped {
		return false
	}
	select {
	case <-cur.exitCh:
		return false
	default:
		return true
	}
}

func (s *managedService) stripEnvKey(key string) {
	prefix := key + "="
	s.mu.Lock()
	defer s.mu.Unlock()
	filtered := s.spec.Env[:0]
	for _, entry := range s.spec.Env {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	s.spec.Env = filtered
}

func (s *managedService) terminateCurrentForReload() {
	s.mu.Lock()
	cur := s.current
	s.mu.Unlock()
	if cur != nil && cur.pid > 0 {
		_ = syscall.Kill(cur.pid, syscall.SIGTERM)
	}
}

type syncLineIoWriter struct {
	w *syncLineWriter
}

func (s syncLineIoWriter) Write(p []byte) (int, error) {
	if s.w != nil {
		s.w.writePrefixedLine("", string(p))
	}
	return len(p), nil
}

// RunSupervisor starts Supervisor, Manager, and Ingress in order with readiness
// gating, supervises and restarts crashed children with exponential backoff,
// and performs ordered drain on ctx cancellation.
func RunSupervisor(ctx context.Context, cfg SupervisorConfig) error {
	tr, err := RunSupervisorWithSlotTransition(ctx, cfg)
	if err != nil {
		return err
	}
	if tr != nil {
		return ExecuteProcessReplacement(tr)
	}
	return nil
}

// RunSupervisorWithSlotTransition runs child supervision and returns a non-nil
// *ExecTransition when an online slot activate, rollback, or 60s trial rollback
// requests an in-place unix.Exec replacement of cpamp-launcher.
func RunSupervisorWithSlotTransition(ctx context.Context, cfg SupervisorConfig) (*ExecTransition, error) {
	cfg.Timings = cfg.Timings.withDefaults(os.Getenv)
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}

	sink := newLineSink()
	defer sink.flushAndStop(cfg.Timings.SIGKILLReapWait)
	stdoutW := &syncLineWriter{sink: sink, dst: cfg.Stdout}
	stderrW := &syncLineWriter{sink: sink, dst: cfg.Stderr}
	fixedBudget := ShutdownFixedBudget()
	stderrW.writePrefixedLine("[launcher] ", fmt.Sprintf(
		"shutdown budget: drain=%s fixed=%s worst-case=%s; container stop grace must be >= drain+%s (>= %s)",
		cfg.Timings.DrainTimeout,
		fixedBudget,
		cfg.Timings.DrainTimeout+fixedBudget,
		ShutdownFixedBudgetLimit,
		cfg.Timings.DrainTimeout+ShutdownFixedBudgetLimit,
	))

	reaper := newCentralReaper()
	defer reaper.stop()

	shutdownCtx, beginShutdown := context.WithCancel(context.Background())
	defer beginShutdown()

	fatalErrCh := make(chan error, 1)

	supSvc := newManagedService(cfg.SupervisorSpec)
	mgrSvc := newManagedService(cfg.ManagerSpec)
	ingSvc := newManagedService(cfg.IngressSpec)
	var upgradeSvc *managedService
	if cfg.UpgradeSpec != nil {
		upgradeSvc = newManagedService(*cfg.UpgradeSpec)
	}

	var (
		trialMu       sync.Mutex
		trialFailCode string
		trialDoneCh   = make(chan struct{})
		trialStopOnce sync.Once
	)
	stopTrialWatchdog := func() {
		trialStopOnce.Do(func() {
			close(trialDoneCh)
		})
	}
	defer stopTrialWatchdog()

	recordTrialFailure := func(code string) {
		trialMu.Lock()
		if trialFailCode == "" {
			trialFailCode = code
		}
		trialMu.Unlock()
		beginShutdown()
	}
	getTrialFailure := func() string {
		trialMu.Lock()
		defer trialMu.Unlock()
		return trialFailCode
	}

	var initialTrialState SlotState
	if cfg.Trial != nil {
		initialTrialState = SlotState{
			SchemaVersion:            1,
			State:                    SlotStateTrial,
			ActiveSlot:               firstNonEmptyString(cfg.ActiveSlot, cfg.Trial.ActiveSlot),
			BaseVersionAtSwitch:      firstNonEmptyString(cfg.BaseVersion, cfg.Trial.BaseVersion),
			SchemaGeneration:         1,
			PreviousSchemaGeneration: 1,
		}
		trialStore := cfg.SlotStore
		if trialStore == nil {
			trialStore = cfg.Trial.Store
		}
		if trialStore != nil {
			if st, exists, err := trialStore.ReadSlotStateIfExists(); err == nil && exists {
				initialTrialState = st
			}
		}
	}

	checkTrialProcessesHealthy := func() string {
		reaper.reapAvailable()
		if failCode := getTrialFailure(); failCode != "" {
			return failCode
		}
		if supSvc.startCount() != 1 || supSvc.nonZeroFailureCount() > 0 || !supSvc.isCurrentlyRunning() {
			return FailureCodeSupervisorStartupFailed
		}
		if upgradeSvc != nil && (upgradeSvc.nonZeroFailureCount() > 0 || upgradeSvc.startCount() > 1) {
			return FailureCodeManagerStartupFailed
		}
		if mgrSvc.startCount() != 1 || mgrSvc.nonZeroFailureCount() > 0 || !mgrSvc.isCurrentlyRunning() {
			return FailureCodeManagerStartupFailed
		}
		if ingSvc.startCount() != 1 || ingSvc.nonZeroFailureCount() > 0 || !ingSvc.isCurrentlyRunning() {
			return FailureCodeIngressStartupFailed
		}
		return ""
	}
	if cfg.Trial != nil {
		var upgradeExitCh <-chan struct{}
		if upgradeSvc != nil {
			upgradeExitCh = upgradeSvc.exitNotify
		}
		go func() {
			timer := time.NewTimer(cfg.Timings.TrialWatchdogTimeout)
			defer timer.Stop()
			for {
				if shutdownCtx.Err() == nil && ctx.Err() == nil {
					if supSvc.nonZeroFailureCount() > 0 || supSvc.startCount() > 1 || (supSvc.startCount() == 1 && !supSvc.isCurrentlyRunning()) {
						recordTrialFailure(FailureCodeSupervisorStartupFailed)
						return
					}
					if upgradeSvc != nil && (upgradeSvc.nonZeroFailureCount() > 0 || upgradeSvc.startCount() > 1) {
						recordTrialFailure(FailureCodeManagerStartupFailed)
						return
					}
					if mgrSvc.nonZeroFailureCount() > 0 || mgrSvc.startCount() > 1 || (mgrSvc.startCount() == 1 && !mgrSvc.isCurrentlyRunning()) {
						recordTrialFailure(FailureCodeManagerStartupFailed)
						return
					}
					if ingSvc.nonZeroFailureCount() > 0 || ingSvc.startCount() > 1 || (ingSvc.startCount() == 1 && !ingSvc.isCurrentlyRunning()) {
						recordTrialFailure(FailureCodeIngressStartupFailed)
						return
					}
				}
				select {
				case <-trialDoneCh:
					return
				case <-shutdownCtx.Done():
					return
				case <-ctx.Done():
					return
				case <-supSvc.exitNotify:
				case <-upgradeExitCh:
				case <-mgrSvc.exitNotify:
				case <-ingSvc.exitNotify:
				case <-timer.C:
					recordTrialFailure(FailureCodeTrialTimeout)
					return
				}
			}
		}()
	}

	shutdownControlServer := func() {
		if cfg.SlotControl != nil {
			ctrlCtx, ctrlCancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = cfg.SlotControl.Shutdown(ctrlCtx)
			ctrlCancel()
		}
	}
	defer shutdownControlServer()

	shutdownAll := func() {
		var upgradeStopped chan struct{}
		if upgradeSvc != nil {
			upgradeSvc.mu.Lock()
			upgradeSvc.stopped = true
			upgradeSvc.mu.Unlock()
			upgradeStopped = make(chan struct{})
			go func() {
				defer close(upgradeStopped)
				stopSingleService(upgradeSvc, reaper, cfg.Timings.ManagerShutdownTimeout, false, 0, cfg.Timings.SIGKILLReapWait, cfg.Timings.ChildIODrainTimeout, stderrW)
			}()
		}
		shutdownServices(ingSvc, mgrSvc, supSvc, reaper, cfg.Timings, stderrW)
		if upgradeStopped != nil {
			<-upgradeStopped
		}
	}

	rollbackTrialAndReturn := func(code string) (*ExecTransition, error) {
		stopTrialWatchdog()
		shutdownControlServer()
		beginShutdown()
		shutdownAll()
		reaper.stop()
		if cfg.OnBeforeExec != nil {
			cfg.OnBeforeExec()
		}
		sink.flushAndStop(cfg.Timings.SIGKILLReapWait)
		tr, _, rbErr := ExecuteTrialRollback(*cfg.Trial, initialTrialState, code, cfg.Stderr)
		if rbErr != nil {
			return nil, rbErr
		}
		return tr, nil
	}

	shutdownAndReturn := func() (*ExecTransition, error) {
		if cfg.Trial != nil {
			if failCode := getTrialFailure(); failCode != "" {
				return rollbackTrialAndReturn(failCode)
			}
		}
		beginShutdown()
		shutdownControlServer()
		shutdownAll()
		select {
		case err := <-fatalErrCh:
			return nil, err
		default:
			return nil, nil
		}
	}

	supFirstStart := make(chan struct{})
	go superviseService(
		shutdownCtx,
		supSvc,
		reaper,
		cfg,
		stdoutW,
		stderrW,
		true,
		supFirstStart,
		func(err error) {
			if cfg.Trial != nil {
				recordTrialFailure(FailureCodeSupervisorStartupFailed)
			}
			select {
			case fatalErrCh <- err:
			default:
			}
			beginShutdown()
		},
	)

	select {
	case <-supFirstStart:
	case <-shutdownCtx.Done():
		return shutdownAndReturn()
	case err := <-fatalErrCh:
		if cfg.Trial != nil {
			return rollbackTrialAndReturn(FailureCodeSupervisorStartupFailed)
		}
		shutdownAll()
		return nil, err
	case <-ctx.Done():
		return shutdownAndReturn()
	}

	if cfg.SupervisorReadyFunc != nil || cfg.SupervisorTCPAddr != "" {
		supReadyCheck := cfg.SupervisorReadyFunc
		if supReadyCheck == nil {
			supReadyCheck = func(checkCtx context.Context) bool {
				return tcpProbeReady(checkCtx, cfg.SupervisorTCPAddr, cfg.Timings.ReadinessPollInterval)
			}
		}
		if err := waitForReadiness(ctx, shutdownCtx, "runtime supervisor", stderrW, supReadyCheck, cfg.Timings.ReadinessPollInterval); err != nil {
			return shutdownAndReturn()
		}
	}

	if cfg.Trial != nil && getTrialFailure() != "" {
		return rollbackTrialAndReturn(getTrialFailure())
	}

	select {
	case err := <-fatalErrCh:
		if cfg.Trial != nil {
			return rollbackTrialAndReturn(FailureCodeSupervisorStartupFailed)
		}
		shutdownAll()
		return nil, err
	case <-ctx.Done():
		return shutdownAndReturn()
	default:
	}

	if upgradeSvc != nil {
		upgradeFirstStart := make(chan struct{})
		go superviseService(shutdownCtx, upgradeSvc, reaper, cfg, stdoutW, stderrW, false, upgradeFirstStart, nil)
		ready, warnReason, fatalErr, canceled := waitUpgradeWorkerStartup(
			ctx,
			shutdownCtx,
			upgradeSvc,
			upgradeFirstStart,
			fatalErrCh,
			cfg.UpgradeSocket,
			cfg.Timings,
		)
		if fatalErr != nil {
			if cfg.Trial != nil {
				return rollbackTrialAndReturn(FailureCodeSupervisorStartupFailed)
			}
			shutdownAll()
			return nil, fatalErr
		}
		if canceled {
			return shutdownAndReturn()
		}
		if !ready {
			if cfg.Trial != nil {
				return rollbackTrialAndReturn(FailureCodeManagerStartupFailed)
			}
			stderrW.writePrefixedLine("[launcher] ", fmt.Sprintf(
				"upgrade worker not ready (%s); proceeding to start manager and ingress in degraded recovery mode",
				warnReason,
			))
		}
	}

	mgrFirstStart := make(chan struct{})
	go superviseService(
		shutdownCtx,
		mgrSvc,
		reaper,
		cfg,
		stdoutW,
		stderrW,
		false,
		mgrFirstStart,
		nil,
	)

	select {
	case <-mgrFirstStart:
	case <-shutdownCtx.Done():
		return shutdownAndReturn()
	case err := <-fatalErrCh:
		if cfg.Trial != nil {
			return rollbackTrialAndReturn(FailureCodeSupervisorStartupFailed)
		}
		shutdownAll()
		return nil, err
	case <-ctx.Done():
		return shutdownAndReturn()
	}

	if cfg.ManagerHealthURL != "" {
		bootstrapURL := managerBootstrapURL(cfg.ManagerHealthURL)
		if cfg.Trial != nil {
			bootstrapURL = ""
		}
		if err := waitForReadiness(ctx, shutdownCtx, "manager", stderrW, func(checkCtx context.Context) bool {
			if cfg.Trial != nil {
				return trialManagerHealthOK(checkCtx, trialReadinessHTTPClient(cfg.Timings.ReadinessPollInterval), cfg.ManagerHealthURL)
			}
			return httpEndpointStatusOK(checkCtx, readinessHTTPClient(cfg.Timings.ReadinessPollInterval), cfg.ManagerHealthURL, bootstrapURL)
		}, cfg.Timings.ReadinessPollInterval); err != nil {
			return shutdownAndReturn()
		}
	}

	if cfg.Trial != nil && getTrialFailure() != "" {
		return rollbackTrialAndReturn(getTrialFailure())
	}

	select {
	case err := <-fatalErrCh:
		if cfg.Trial != nil {
			return rollbackTrialAndReturn(FailureCodeSupervisorStartupFailed)
		}
		shutdownAll()
		return nil, err
	case <-ctx.Done():
		return shutdownAndReturn()
	default:
	}

	ingFirstStart := make(chan struct{})
	go superviseService(
		shutdownCtx,
		ingSvc,
		reaper,
		cfg,
		stdoutW,
		stderrW,
		false,
		ingFirstStart,
		nil,
	)

	if cfg.Trial != nil || cfg.OnAllReady != nil {
		select {
		case <-ingFirstStart:
		case <-shutdownCtx.Done():
			return shutdownAndReturn()
		case err := <-fatalErrCh:
			if cfg.Trial != nil {
				return rollbackTrialAndReturn(FailureCodeSupervisorStartupFailed)
			}
			shutdownAll()
			return nil, err
		case <-ctx.Done():
			return shutdownAndReturn()
		}

		pubProbeAddr := NormalizeTCPProbeAddr(cfg.IngressPublicTCPAddr)
		if pubProbeAddr != "" {
			if err := waitForReadiness(ctx, shutdownCtx, "ingress public", stderrW, func(checkCtx context.Context) bool {
				return tcpProbeReady(checkCtx, pubProbeAddr, cfg.Timings.ReadinessPollInterval)
			}, cfg.Timings.ReadinessPollInterval); err != nil {
				return shutdownAndReturn()
			}
		}
		adminProbeAddr := NormalizeTCPProbeAddr(cfg.IngressAdminTCPAddr)
		if adminProbeAddr != "" {
			if err := waitForReadiness(ctx, shutdownCtx, "ingress admin", stderrW, func(checkCtx context.Context) bool {
				return tcpProbeReady(checkCtx, adminProbeAddr, cfg.Timings.ReadinessPollInterval)
			}, cfg.Timings.ReadinessPollInterval); err != nil {
				return shutdownAndReturn()
			}
		}
	}

	if cfg.Trial != nil {
		if failCode := runTrialStabilizationSoak(ctx, shutdownCtx, cfg, reaper, supSvc, mgrSvc, ingSvc, fatalErrCh, getTrialFailure); failCode != "" {
			if failCode == "canceled" {
				return shutdownAndReturn()
			}
			return rollbackTrialAndReturn(failCode)
		}
		if cfg.beforeTrialCommit != nil {
			cfg.beforeTrialCommit()
		}
		var preCommitFailCode string
		committedState, commitErr := persistCommittedTrialState(*cfg.Trial, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if code := checkTrialProcessesHealthy(); code != "" {
				preCommitFailCode = code
				return fmt.Errorf("trial child unhealthy before commit: %s", code)
			}
			return nil
		})
		if commitErr != nil {
			if ctx.Err() != nil && preCommitFailCode == "" && getTrialFailure() == "" {
				return shutdownAndReturn()
			}
			code := firstNonEmptyString(preCommitFailCode, getTrialFailure(), SlotCodeInternalError)
			return rollbackTrialAndReturn(code)
		}
		if code := checkTrialProcessesHealthy(); code != "" {
			return rollbackTrialAndReturn(code)
		}
		stopTrialWatchdog()
		mgrSvc.stripEnvKey("CPAMP_TRIAL_HOLD")
		released := finalizeCommittedTrialRelease(ctx, *cfg.Trial, committedState, syncLineIoWriter{w: stderrW})
		if !released {
			mgrSvc.terminateCurrentForReload()
		}
	}

	if cfg.OnAllReady != nil {
		cfg.OnAllReady()
	}

	var switchCh <-chan SlotSwitchAction
	if cfg.SlotControl != nil {
		switchCh = cfg.SlotControl.SwitchChan()
	}

	select {
	case action := <-switchCh:
		shutdownControlServer()
		beginShutdown()
		shutdownAll()
		reaper.stop()
		if cfg.OnBeforeExec != nil {
			cfg.OnBeforeExec()
		}
		if cfg.SlotStore != nil {
			if err := WriteSwitchSlotState(cfg.SlotStore, cfg.BaseVersion, cfg.ActiveSlot, action); err != nil {
				return nil, fmt.Errorf("failed writing slot-state for transition: %w", err)
			}
		}
		var nextActiveSlot string
		if action.Kind == SlotSwitchActivate {
			nextActiveSlot = action.TargetVersion
		} else {
			nextActiveSlot = action.RollbackTargetSlot
		}
		sink.flushAndStop(cfg.Timings.SIGKILLReapWait)
		return &ExecTransition{
			Path: action.TargetLauncherPath,
			Args: cloneArgsOrDefault(cfg.LauncherArgs, action.TargetLauncherPath),
			Env:  buildLauncherExecEnv(cfg.BaseEnviron, cfg.BaseInstallRoot, cfg.BaseVersion, nextActiveSlot),
		}, nil
	case err := <-fatalErrCh:
		beginShutdown()
		shutdownControlServer()
		shutdownAll()
		return nil, err
	case <-ctx.Done():
		return shutdownAndReturn()
	}
}

func superviseService(
	shutdownCtx context.Context,
	svc *managedService,
	reaper *centralReaper,
	cfg SupervisorConfig,
	stdoutW, stderrW *syncLineWriter,
	isSupervisor bool,
	firstStart chan struct{},
	onFatal func(error),
) {
	defer close(svc.doneCh)
	var firstStartOnce sync.Once
	notifyStarted := func() {
		if firstStart != nil {
			firstStartOnce.Do(func() { close(firstStart) })
		}
	}

	backoff := cfg.Timings.InitialBackoff
	consecutiveShortFailures := 0

	for {
		if shutdownCtx.Err() != nil {
			notifyStarted()
			return
		}

		svc.mu.Lock()
		if svc.stopped {
			svc.mu.Unlock()
			notifyStarted()
			return
		}
		spec := svc.spec
		var err error
		if isSupervisor && cfg.BeforeSupervisorStart != nil {
			spec, err = cfg.BeforeSupervisorStart(withChildReaper(shutdownCtx, reaper.reapAvailable), spec)
		}
		var recordExitOnce sync.Once
		recordChildExit := func(status syscall.WaitStatus, reaped bool) {
			recordExitOnce.Do(func() {
				svc.mu.Lock()
				svc.reapedCount++
				if !reaped || !status.Exited() || status.ExitStatus() != 0 {
					svc.nonZeroExits++
				}
				svc.mu.Unlock()
				svc.notifyExit()
			})
		}
		var child *runningChild
		if err == nil && shutdownCtx.Err() == nil {
			child, err = reaper.startChild(spec, cfg.SetCredentials, stdoutW, stderrW, func(st syscall.WaitStatus) {
				recordChildExit(st, true)
			})
		} else if err == nil {
			err = shutdownCtx.Err()
		}
		if err == nil {
			svc.starts++
			svc.current = child
		} else {
			svc.current = nil
		}
		svc.mu.Unlock()
		var uptime time.Duration
		if err != nil {
			stderrW.writePrefixedLine("[launcher] ", fmt.Sprintf("failed to start %s: %v", svc.spec.Name, err))
			uptime = 0
			svc.recordNonZeroExit()
		} else {
			if cfg.OnChildStart != nil {
				cfg.OnChildStart(svc.spec.Name, child.pid)
			}
			notifyStarted()
			<-child.exitCh
			uptime = time.Since(child.startedAt)
			recordChildExit(child.waitStatus, child.reaped)
			if isSupervisor {
				terminateAndReapProcessGroup(child.pgid, reaper, cfg.Timings.ProcessGroupKillGrace, cfg.Timings.SIGKILLReapWait)
			}
			waitChildIODone(child, svc.spec.Name, cfg.Timings.ChildIODrainTimeout, stderrW)
			svc.mu.Lock()
			if svc.current == child {
				svc.current = nil
			}
			svc.mu.Unlock()
		}

		svc.mu.Lock()
		stopped := svc.stopped
		svc.mu.Unlock()
		if stopped || shutdownCtx.Err() != nil {
			return
		}

		if isSupervisor {
			if uptime < cfg.Timings.SupervisorShortRunWindow {
				consecutiveShortFailures++
				if consecutiveShortFailures >= cfg.Timings.SupervisorMaxConsecutiveShortFailures {
					if onFatal != nil {
						onFatal(fmt.Errorf(
							"supervisor failed %d consecutive starts under %s",
							consecutiveShortFailures,
							cfg.Timings.SupervisorShortRunWindow,
						))
					}
					return
				}
			} else {
				consecutiveShortFailures = 0
			}
		}

		var waitDur time.Duration
		waitDur, backoff = nextRestartBackoff(backoff, uptime, cfg.Timings)
		timer := time.NewTimer(waitDur)
		select {
		case <-shutdownCtx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// nextRestartBackoff computes the wait duration before the next restart and the
// subsequent exponential backoff state. If the exited process ran for at least
// timings.HealthyUptimeReset, the backoff resets to timings.InitialBackoff first.
func nextRestartBackoff(currentBackoff, uptime time.Duration, timings TimingConfig) (waitDur, nextBackoff time.Duration) {
	if uptime >= timings.HealthyUptimeReset {
		currentBackoff = timings.InitialBackoff
	}
	waitDur = currentBackoff
	nextBackoff = currentBackoff * 2
	if nextBackoff > timings.MaxBackoff {
		nextBackoff = timings.MaxBackoff
	}
	return waitDur, nextBackoff
}

func shutdownServices(
	ingSvc, mgrSvc, supSvc *managedService,
	reaper *centralReaper,
	timings TimingConfig,
	stderrW *syncLineWriter,
) {
	for _, svc := range []*managedService{ingSvc, mgrSvc, supSvc} {
		if svc == nil {
			continue
		}
		svc.mu.Lock()
		svc.stopped = true
		svc.mu.Unlock()
	}

	// 1. SIGTERM Ingress; wait up to drain timeout + 5s; SIGKILL if still alive.
	stopSingleService(ingSvc, reaper, timings.DrainTimeout+timings.IngressDrainGraceExtra, false, 0, timings.SIGKILLReapWait, timings.ChildIODrainTimeout, stderrW)
	// 2. SIGTERM Manager; wait up to 15s; then SIGKILL.
	stopSingleService(mgrSvc, reaper, timings.ManagerShutdownTimeout, false, 0, timings.SIGKILLReapWait, timings.ChildIODrainTimeout, stderrW)
	// 3. SIGTERM Supervisor; wait up to 15s; then kill and reap its process group (CPA).
	stopSingleService(supSvc, reaper, timings.SupervisorShutdownTimeout, true, timings.ProcessGroupKillGrace, timings.SIGKILLReapWait, timings.ChildIODrainTimeout, stderrW)
}

func stopSingleService(
	svc *managedService,
	reaper *centralReaper,
	timeout time.Duration,
	killProcessGroupAfter bool,
	pgKillGrace time.Duration,
	sigkillReapWait time.Duration,
	ioDrainTimeout time.Duration,
	stderrW *syncLineWriter,
) {
	if svc == nil {
		return
	}
	svc.mu.Lock()
	child := svc.current
	svc.mu.Unlock()
	if child == nil {
		return
	}

	_ = syscall.Kill(child.pid, syscall.SIGTERM)

	timer := time.NewTimer(timeout)
	select {
	case <-child.exitCh:
		timer.Stop()
	case <-timer.C:
		_ = syscall.Kill(-child.pgid, syscall.SIGKILL)
		_ = syscall.Kill(child.pid, syscall.SIGKILL)
		if !waitChildExitAfterSIGKILL(child, reaper, sigkillReapWait) {
			stderrW.writePrefixedLine("[launcher] ", fmt.Sprintf(
				"%s (pid %d) did not exit within %s after SIGKILL; continuing shutdown",
				svc.spec.Name,
				child.pid,
				sigkillReapWait,
			))
		}
	}

	if killProcessGroupAfter {
		terminateAndReapProcessGroup(child.pgid, reaper, pgKillGrace, sigkillReapWait)
	}
	waitChildIODone(child, svc.spec.Name, ioDrainTimeout, stderrW)
}

func waitChildExitAfterSIGKILL(child *runningChild, reaper *centralReaper, wait time.Duration) bool {
	if wait <= 0 {
		wait = SIGKILLReapWait
	}
	deadline := time.Now().Add(wait)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		reaper.reapAvailable()
		select {
		case <-child.exitCh:
			return true
		default:
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-child.exitCh:
			return true
		case <-ticker.C:
		}
	}
}

func waitChildIODone(child *runningChild, name string, timeout time.Duration, stderrW *syncLineWriter) {
	if child == nil {
		return
	}
	if timeout <= 0 {
		timeout = ChildIODrainTimeout
	}
	timer := time.NewTimer(timeout)
	select {
	case <-child.ioDone:
		timer.Stop()
	case <-timer.C:
		stderrW.writePrefixedLine("[launcher] ", fmt.Sprintf(
			"%s (pid %d) stdio did not close within %s; detaching output pipes and continuing",
			name,
			child.pid,
			timeout,
		))
		child.closeOutputPipes()
	}
}

func terminateAndReapProcessGroup(pgid int, reaper *centralReaper, grace, sigkillWait time.Duration) {
	if pgid <= 0 {
		return
	}
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil && errors.Is(err, syscall.ESRCH) {
		reaper.reapAvailable()
		return
	}
	if grace <= 0 {
		grace = 100 * time.Millisecond
	}
	if sigkillWait <= 0 {
		sigkillWait = SIGKILLReapWait
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		reaper.reapAvailable()
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	killDeadline := time.Now().Add(sigkillWait)
	for time.Now().Before(killDeadline) {
		reaper.reapAvailable()
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForReadiness blocks until check succeeds, logging periodically. It returns
// only when the launcher is shutting down, so a downstream service never starts
// before its upstream is ready.
func waitForReadiness(ctx, shutdownCtx context.Context, label string, stderrW *syncLineWriter, check func(context.Context) bool, interval time.Duration) error {
	if interval <= 0 {
		interval = DefaultReadinessPollInterval
	}
	start := time.Now()
	nextLog := start.Add(ReadinessLogInterval)
	for {
		if check(ctx) {
			return nil
		}
		select {
		case <-shutdownCtx.Done():
			return shutdownCtx.Err()
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
		if !time.Now().Before(nextLog) {
			stderrW.writePrefixedLine("[launcher] ", fmt.Sprintf("%s is not ready after %s; still waiting", label, time.Since(start).Round(time.Second)))
			nextLog = time.Now().Add(ReadinessLogInterval)
		}
	}
}

func waitUpgradeWorkerStartup(
	ctx, shutdownCtx context.Context,
	svc *managedService,
	firstStart <-chan struct{},
	fatalErrCh <-chan error,
	socketPath string,
	timings TimingConfig,
) (ready bool, reason string, fatalErr error, canceled bool) {
	timeout := timings.UpgradeWorkerReadyTimeout
	if timeout <= 0 {
		timeout = UpgradeWorkerReadyTimeout
	}
	maxFailures := timings.UpgradeWorkerMaxInitialFailures
	if maxFailures <= 0 {
		maxFailures = UpgradeWorkerMaxInitialFailures
	}
	pollInterval := timings.ReadinessPollInterval
	if pollInterval <= 0 {
		pollInterval = DefaultReadinessPollInterval
	}

	deadlineTimer := time.NewTimer(timeout)
	defer deadlineTimer.Stop()

	started := false
	for {
		if !started {
			select {
			case <-firstStart:
				started = true
			default:
			}
		}
		if !started {
			if failures := svc.nonZeroFailureCount(); failures >= maxFailures {
				return false, fmt.Sprintf("exited non-zero %d times before starting", failures), nil, false
			}
			select {
			case <-firstStart:
				started = true
				continue
			case <-svc.exitNotify:
				continue
			case <-deadlineTimer.C:
				return false, fmt.Sprintf("did not start within %s", timeout), nil, false
			case err := <-fatalErrCh:
				return false, "", err, false
			case <-shutdownCtx.Done():
				return false, "", nil, true
			case <-ctx.Done():
				return false, "", nil, true
			}
		}

		if socketPath == "" {
			return true, "", nil, false
		}
		dialer := net.Dialer{Timeout: pollInterval * 2}
		if conn, err := dialer.DialContext(ctx, "unix", socketPath); err == nil {
			_ = conn.Close()
			return true, "", nil, false
		}
		if failures := svc.nonZeroFailureCount(); failures >= maxFailures {
			return false, fmt.Sprintf("exited non-zero %d times before socket readiness", failures), nil, false
		}

		pollTimer := time.NewTimer(pollInterval)
		select {
		case <-pollTimer.C:
		case <-svc.exitNotify:
			pollTimer.Stop()
		case <-deadlineTimer.C:
			pollTimer.Stop()
			return false, fmt.Sprintf("socket not ready after %s", timeout), nil, false
		case err := <-fatalErrCh:
			pollTimer.Stop()
			return false, "", err, false
		case <-shutdownCtx.Done():
			pollTimer.Stop()
			return false, "", nil, true
		case <-ctx.Done():
			pollTimer.Stop()
			return false, "", nil, true
		}
	}
}

func tcpProbeReady(_ context.Context, addr string, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = DefaultReadinessPollInterval
	}
	conn, err := net.DialTimeout("tcp", addr, timeout*2)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func readinessHTTPClient(interval time.Duration) *http.Client {
	if interval <= 0 {
		interval = DefaultReadinessPollInterval
	}
	return &http.Client{Timeout: interval * 2}
}

func trialReadinessHTTPClient(interval time.Duration) *http.Client {
	if interval <= 0 {
		interval = DefaultReadinessPollInterval
	}
	timeout := max(interval*2, 500*time.Millisecond)
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func trialManagerHealthOK(ctx context.Context, client *http.Client, healthURL string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var payload struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&payload); err != nil {
		return false
	}
	return payload.OK
}

func runTrialStabilizationSoak(
	ctx context.Context,
	shutdownCtx context.Context,
	cfg SupervisorConfig,
	reaper *centralReaper,
	supSvc, mgrSvc, ingSvc *managedService,
	fatalErrCh <-chan error,
	getTrialFailure func() string,
) string {
	pubProbeAddr := NormalizeTCPProbeAddr(cfg.IngressPublicTCPAddr)
	adminProbeAddr := NormalizeTCPProbeAddr(cfg.IngressAdminTCPAddr)
	probeTimeout := max(cfg.Timings.ReadinessPollInterval, 250*time.Millisecond)
	httpClient := trialReadinessHTTPClient(cfg.Timings.ReadinessPollInterval)

	checkProcesses := func() string {
		if reaper != nil {
			reaper.reapAvailable()
		}
		if failCode := getTrialFailure(); failCode != "" {
			return failCode
		}
		if supSvc.startCount() != 1 || supSvc.nonZeroFailureCount() > 0 || !supSvc.isCurrentlyRunning() {
			return FailureCodeSupervisorStartupFailed
		}
		if mgrSvc.startCount() != 1 || mgrSvc.nonZeroFailureCount() > 0 || !mgrSvc.isCurrentlyRunning() {
			return FailureCodeManagerStartupFailed
		}
		if ingSvc.startCount() != 1 || ingSvc.nonZeroFailureCount() > 0 || !ingSvc.isCurrentlyRunning() {
			return FailureCodeIngressStartupFailed
		}
		return ""
	}

	checkOnce := func() string {
		if failCode := checkProcesses(); failCode != "" {
			return failCode
		}
		if cfg.SupervisorTCPAddr != "" && !tcpProbeReady(ctx, cfg.SupervisorTCPAddr, probeTimeout) {
			return FailureCodeSupervisorStartupFailed
		}
		if cfg.ManagerHealthURL != "" && !trialManagerHealthOK(ctx, httpClient, cfg.ManagerHealthURL) {
			return FailureCodeManagerStartupFailed
		}
		if pubProbeAddr != "" && !tcpProbeReady(ctx, pubProbeAddr, probeTimeout) {
			return FailureCodeIngressStartupFailed
		}
		if adminProbeAddr != "" && !tcpProbeReady(ctx, adminProbeAddr, probeTimeout) {
			return FailureCodeIngressStartupFailed
		}
		return checkProcesses()
	}

	if failCode := checkOnce(); failCode != "" {
		return failCode
	}
	soakDur := cfg.Timings.TrialStabilizationWindow
	if soakDur <= 0 {
		return ""
	}

	pollInterval := cfg.Timings.ReadinessPollInterval
	if pollInterval <= 0 {
		pollInterval = DefaultReadinessPollInterval
	}
	if pollInterval > soakDur {
		pollInterval = soakDur
	}

	soakTimer := time.NewTimer(soakDur)
	defer soakTimer.Stop()
	pollTicker := time.NewTicker(pollInterval)
	defer pollTicker.Stop()

	for {
		select {
		case <-soakTimer.C:
			return checkOnce()
		case <-pollTicker.C:
			if failCode := checkOnce(); failCode != "" {
				return failCode
			}
		case <-supSvc.exitNotify:
			if failCode := checkProcesses(); failCode != "" {
				return failCode
			}
		case <-mgrSvc.exitNotify:
			if failCode := checkProcesses(); failCode != "" {
				return failCode
			}
		case <-ingSvc.exitNotify:
			if failCode := checkProcesses(); failCode != "" {
				return failCode
			}
		case <-fatalErrCh:
			return FailureCodeSupervisorStartupFailed
		case <-shutdownCtx.Done():
			if failCode := getTrialFailure(); failCode != "" {
				return failCode
			}
			return "canceled"
		case <-ctx.Done():
			if failCode := getTrialFailure(); failCode != "" {
				return failCode
			}
			return "canceled"
		}
	}
}
func managerBootstrapURL(healthURL string) string {
	if strings.HasSuffix(healthURL, "/health") {
		return strings.TrimSuffix(healthURL, "/health") + "/.well-known/cpamp-bootstrap"
	}
	return ""
}

func httpEndpointStatusOK(ctx context.Context, client *http.Client, healthURL, bootstrapURL string) bool {
	status, ok := httpGetStatus(ctx, client, healthURL)
	if !ok {
		return false
	}
	if status == http.StatusOK {
		return true
	}
	if status != http.StatusServiceUnavailable || bootstrapURL == "" {
		return false
	}
	// Check the in-memory read-only shell at /.well-known/cpamp-bootstrap/ui
	// before probing /.well-known/cpamp-bootstrap. Before startup admission
	// settles (and after BeginWritableStartup on store-open failure), /ui
	// answers 404, so Ingress still waits; once read-only discovery is mounted,
	// /ui answers 200 immediately without waiting on a large SQLite snapshot.
	uiStatus, uiOK := httpGetStatus(ctx, client, strings.TrimRight(bootstrapURL, "/")+"/ui")
	if uiOK && uiStatus == http.StatusOK {
		return true
	}
	bootStatus, ok := httpGetStatus(ctx, client, bootstrapURL)
	return ok && bootStatus == http.StatusOK
}

func httpGetStatus(ctx context.Context, client *http.Client, targetURL string) (int, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, false
	}
	_ = resp.Body.Close()
	return resp.StatusCode, true
}

func buildSysProcAttr(spec ChildSpec, setCreds bool) *syscall.SysProcAttr {
	attr := &syscall.SysProcAttr{
		Setpgid: true,
	}
	if setCreds {
		cred := &syscall.Credential{
			Uid: uint32(spec.UID),
			Gid: uint32(spec.GID),
		}
		if spec.ClearSupp {
			cred.Groups = []uint32{}
		}
		attr.Credential = cred
	}
	return attr
}
