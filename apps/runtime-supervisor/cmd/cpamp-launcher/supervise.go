//go:build unix

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
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
	return t
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
	IngressSpec           ChildSpec
	SupervisorTCPAddr     string
	ManagerHealthURL      string
	Timings               TimingConfig
	Stdout                io.Writer
	Stderr                io.Writer
	SetCredentials        bool
	BeforeSupervisorStart func(context.Context, ChildSpec) (ChildSpec, error)
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
	return formatEnvMap(env)
}

// BuildManagerChildEnv builds the environment for cpa-manager-plus by passing
// through the container environment (which carries image ENV defaults and user
// overrides), stripping launcher/Supervisor-only secrets, and forcing the
// single-container loopback addresses and token file path.
func BuildManagerChildEnv(prepared *PreparedRuntime, containerEnv map[string]string) []string {
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
	env["HTTP_ADDR"] = DefaultManagerAddr
	env["CPAMP_RUNTIME_URL"] = "http://" + DefaultRuntimeAddr
	env["CPAMP_RUNTIME_TOKEN_FILE"] = tokenFile
	env["CPAMP_INGRESS_CONTROL_URL"] = "http://" + DefaultIngressAdminAddr

	delete(env, "CPAMP_RUNTIME_TOKEN")
	return formatEnvMap(env)
}

// BuildIngressChildEnv builds the clean environment for cpamp-ingress.
// CPAMP_RUNTIME_TOKEN is never included.
func BuildIngressChildEnv(drainTimeout time.Duration, containerEnv map[string]string) []string {
	if drainTimeout <= 0 {
		drainTimeout = DefaultDrainTimeout
	}
	env := baseScratchEnv(containerEnv)
	env["CPAMP_INGRESS_ADDR"] = DefaultIngressPublicAddr
	env["CPAMP_MANAGER_URL"] = "http://" + DefaultManagerAddr
	env["CPAMP_GATEWAY_URL"] = "http://" + DefaultRuntimeCPAAddr
	env["CPAMP_INGRESS_CONTROL_ADDR"] = DefaultIngressAdminAddr
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
	ch     chan lineWriteReq
	stopCh chan struct{}
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
	children map[int]chan syscall.WaitStatus
	sigchld  chan os.Signal
	stopCh   chan struct{}
	doneCh   chan struct{}
}

func newCentralReaper() *centralReaper {
	sigchld := make(chan os.Signal, 8)
	signal.Notify(sigchld, syscall.SIGCHLD)
	r := &centralReaper{
		children: make(map[int]chan syscall.WaitStatus),
		sigchld:  sigchld,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
	go r.loop()
	return r
}

func (r *centralReaper) stop() {
	signal.Stop(r.sigchld)
	close(r.stopCh)
	<-r.doneCh
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
			if ch, ok := r.children[wpid]; ok {
				delete(r.children, wpid)
				ch <- status
				close(ch)
			}
			r.mu.Unlock()
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
	exitCh         <-chan syscall.WaitStatus
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

	exitCh := make(chan syscall.WaitStatus, 1)

	r.mu.Lock()
	var oldUmask int
	if spec.HasUmask {
		oldUmask = syscall.Umask(spec.Umask)
	}
	startErr := cmd.Start()
	if spec.HasUmask {
		syscall.Umask(oldUmask)
	}
	var pid int
	if startErr == nil && cmd.Process != nil {
		pid = cmd.Process.Pid
		r.children[pid] = exitCh
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

	ioDone := make(chan struct{})
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

	return &runningChild{
		spec:      spec,
		pid:       pid,
		pgid:      pid,
		startedAt: time.Now(),
		exitCh:    exitCh,
		ioDone:    ioDone,
		outR:      outR,
		errR:      errR,
	}, nil
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
	spec    ChildSpec
	mu      sync.Mutex
	current *runningChild
	stopped bool
	doneCh  chan struct{}
}

// RunSupervisor starts Supervisor, Manager, and Ingress in order with readiness
// gating, supervises and restarts crashed children with exponential backoff,
// and performs ordered drain on ctx cancellation.
func RunSupervisor(ctx context.Context, cfg SupervisorConfig) error {
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

	supSvc := &managedService{spec: cfg.SupervisorSpec, doneCh: make(chan struct{})}
	mgrSvc := &managedService{spec: cfg.ManagerSpec, doneCh: make(chan struct{})}
	ingSvc := &managedService{spec: cfg.IngressSpec, doneCh: make(chan struct{})}

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
			select {
			case fatalErrCh <- err:
			default:
			}
			beginShutdown()
		},
	)

	shutdownAndReturn := func() error {
		beginShutdown()
		shutdownServices(ingSvc, mgrSvc, supSvc, reaper, cfg.Timings, stderrW)
		select {
		case err := <-fatalErrCh:
			return err
		default:
			return nil
		}
	}

	select {
	case <-supFirstStart:
	case err := <-fatalErrCh:
		shutdownServices(ingSvc, mgrSvc, supSvc, reaper, cfg.Timings, stderrW)
		return err
	case <-ctx.Done():
		return shutdownAndReturn()
	}

	if cfg.SupervisorTCPAddr != "" {
		if err := waitForReadiness(ctx, shutdownCtx, "runtime supervisor", stderrW, func(checkCtx context.Context) bool {
			return tcpProbeReady(checkCtx, cfg.SupervisorTCPAddr, cfg.Timings.ReadinessPollInterval)
		}, cfg.Timings.ReadinessPollInterval); err != nil {
			return shutdownAndReturn()
		}
	}

	select {
	case err := <-fatalErrCh:
		shutdownServices(ingSvc, mgrSvc, supSvc, reaper, cfg.Timings, stderrW)
		return err
	case <-ctx.Done():
		return shutdownAndReturn()
	default:
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
	case err := <-fatalErrCh:
		shutdownServices(ingSvc, mgrSvc, supSvc, reaper, cfg.Timings, stderrW)
		return err
	case <-ctx.Done():
		return shutdownAndReturn()
	}

	if cfg.ManagerHealthURL != "" {
		bootstrapURL := managerBootstrapURL(cfg.ManagerHealthURL)
		if err := waitForReadiness(ctx, shutdownCtx, "manager", stderrW, func(checkCtx context.Context) bool {
			return httpEndpointStatusOK(checkCtx, readinessHTTPClient(cfg.Timings.ReadinessPollInterval), cfg.ManagerHealthURL, bootstrapURL)
		}, cfg.Timings.ReadinessPollInterval); err != nil {
			return shutdownAndReturn()
		}
	}

	select {
	case err := <-fatalErrCh:
		shutdownServices(ingSvc, mgrSvc, supSvc, reaper, cfg.Timings, stderrW)
		return err
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

	select {
	case err := <-fatalErrCh:
		beginShutdown()
		shutdownServices(ingSvc, mgrSvc, supSvc, reaper, cfg.Timings, stderrW)
		return err
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
		var child *runningChild
		if err == nil && shutdownCtx.Err() == nil {
			child, err = reaper.startChild(spec, cfg.SetCredentials, stdoutW, stderrW)
		} else if err == nil {
			err = shutdownCtx.Err()
		}
		if err == nil {
			svc.current = child
		} else {
			svc.current = nil
		}
		svc.mu.Unlock()
		var uptime time.Duration
		if err != nil {
			stderrW.writePrefixedLine("[launcher] ", fmt.Sprintf("failed to start %s: %v", svc.spec.Name, err))
			uptime = 0
		} else {
			notifyStarted()
			<-child.exitCh
			uptime = time.Since(child.startedAt)
			if isSupervisor {
				terminateAndReapProcessGroup(child.pgid, reaper, cfg.Timings.ProcessGroupKillGrace, cfg.Timings.SIGKILLReapWait)
			}
			waitChildIODone(child, svc.spec.Name, cfg.Timings.ChildIODrainTimeout, stderrW)
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

func managerBootstrapURL(healthURL string) string {
	if strings.HasSuffix(healthURL, "/health") {
		return strings.TrimSuffix(healthURL, "/health") + "/.well-known/cpamp-bootstrap"
	}
	return ""
}

func httpEndpointStatusOK(ctx context.Context, client *http.Client, healthURL, bootstrapURL string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return true
	}
	if resp.StatusCode == http.StatusServiceUnavailable && bootstrapURL != "" {
		bootReq, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, bootstrapURL, nil)
		if reqErr != nil {
			return false
		}
		bootResp, doErr := client.Do(bootReq)
		if doErr != nil {
			return false
		}
		_ = bootResp.Body.Close()
		return bootResp.StatusCode == http.StatusOK
	}
	return false
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
