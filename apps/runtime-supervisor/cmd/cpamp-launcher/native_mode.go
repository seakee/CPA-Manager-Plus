//go:build unix

package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	DefaultNativeRuntimeAddr        = "127.0.0.1:18318"
	DefaultNativeManagerAddr        = "127.0.0.1:18319"
	DefaultNativeIngressControlAddr = "127.0.0.1:18320"
	DefaultNativeIngressPublicAddr  = "0.0.0.0:18317"
	DefaultNativeCPAAddr            = "127.0.0.1:8317"
)

type nativePathsAndAddrs struct {
	HomeDir             string
	BinDir              string
	DataDir             string
	RunDir              string
	LogDir              string
	PIDFile             string
	LogFile             string
	RuntimeDataDir      string
	JournalPath         string
	RuntimeRoot         string
	TokenFile           string
	LauncherTokenFile   string
	BootstrapKeyFile    string
	IngressPublicAddr   string
	RuntimeAddr         string
	ManagerAddr         string
	IngressControlAddr  string
	RuntimeCPAAddr      string
	LauncherControlAddr string
}

func resolveNativePathsAndAddrs(homeDir string, envMap map[string]string) (nativePathsAndAddrs, error) {
	get := func(k string) string { return strings.TrimSpace(envMap[k]) }

	binDir := firstNonEmptyString(get("CPAMP_NATIVE_BIN_DIR"), filepath.Join(homeDir, "bin"))
	dataDir := firstNonEmptyString(get("CPAMP_NATIVE_DATA_DIR"), filepath.Join(homeDir, "data"))
	runDir := firstNonEmptyString(get("CPAMP_NATIVE_RUN_DIR"), filepath.Join(homeDir, "run"))
	logDir := firstNonEmptyString(get("CPAMP_NATIVE_LOG_DIR"), filepath.Join(homeDir, "logs"))
	pidFile := firstNonEmptyString(get("CPAMP_NATIVE_PID_FILE"), filepath.Join(runDir, "cpa-manager-plus.pid"))
	logFile := firstNonEmptyString(get("CPAMP_NATIVE_LOG_FILE"), filepath.Join(logDir, "cpa-manager-plus.log"))
	runtimeDataDir := firstNonEmptyString(get("CPAMP_RUNTIME_DATA_DIR"), filepath.Join(dataDir, "runtime"))
	journalPath := firstNonEmptyString(get("CPAMP_RUNTIME_JOURNAL_PATH"), filepath.Join(runtimeDataDir, "supervisor", "operations.sqlite"))
	runtimeRoot := filepath.Dir(filepath.Dir(filepath.Clean(journalPath)))
	tokenFile := firstNonEmptyString(get("CPAMP_RUNTIME_TOKEN_FILE"), filepath.Join(runDir, "runtime-token"))
	launcherTokenFile := filepath.Join(filepath.Dir(tokenFile), "launcher-token")
	bootstrapKeyFile := filepath.Join(runDir, "gateway-bootstrap-key")

	ingressAddr, err := resolveNativeIngressAddr(envMap)
	if err != nil {
		return nativePathsAndAddrs{}, err
	}
	runtimeAddr, err := resolveNativeLoopbackServiceAddr(
		"runtime-supervisor",
		get("CPAMP_RUNTIME_ADDR"),
		get("CPAMP_RUNTIME_PORT"),
		get("CPAMP_RUNTIME_URL"),
		DefaultNativeRuntimeAddr,
	)
	if err != nil {
		return nativePathsAndAddrs{}, err
	}
	managerAddr, err := resolveNativeLoopbackServiceAddr(
		"manager",
		get("CPAMP_MANAGER_ADDR"),
		get("CPAMP_MANAGER_PORT"),
		get("CPAMP_MANAGER_URL"),
		DefaultNativeManagerAddr,
	)
	if err != nil {
		return nativePathsAndAddrs{}, err
	}
	ingressControlAddr, err := resolveNativeLoopbackServiceAddr(
		"ingress-control",
		get("CPAMP_INGRESS_CONTROL_ADDR"),
		get("CPAMP_INGRESS_CONTROL_PORT"),
		get("CPAMP_INGRESS_CONTROL_URL"),
		DefaultNativeIngressControlAddr,
	)
	if err != nil {
		return nativePathsAndAddrs{}, err
	}
	cpaURLFallback := get("CPAMP_GATEWAY_URL")
	cpaAddr, err := resolveNativeLoopbackServiceAddr(
		"cpa",
		get("CPAMP_RUNTIME_CPA_ADDR"),
		get("CPAMP_CPA_PORT"),
		cpaURLFallback,
		DefaultNativeCPAAddr,
	)
	if err != nil {
		return nativePathsAndAddrs{}, err
	}
	launcherControlAddr, err := ValidateLoopbackListenAddr(firstNonEmptyString(get("CPAMP_LAUNCHER_CONTROL_ADDR"), DefaultLauncherControlAddr))
	if err != nil {
		return nativePathsAndAddrs{}, err
	}

	if err := checkDistinctPorts(map[string]string{
		"ingress":            ingressAddr,
		"runtime-supervisor": runtimeAddr,
		"manager":            managerAddr,
		"ingress-control":    ingressControlAddr,
		"cpa":                cpaAddr,
		"launcher-control":   launcherControlAddr,
	}); err != nil {
		return nativePathsAndAddrs{}, err
	}

	return nativePathsAndAddrs{
		HomeDir:             filepath.Clean(homeDir),
		BinDir:              filepath.Clean(binDir),
		DataDir:             filepath.Clean(dataDir),
		RunDir:              filepath.Clean(runDir),
		LogDir:              filepath.Clean(logDir),
		PIDFile:             filepath.Clean(pidFile),
		LogFile:             filepath.Clean(logFile),
		RuntimeDataDir:      filepath.Clean(runtimeDataDir),
		JournalPath:         filepath.Clean(journalPath),
		RuntimeRoot:         filepath.Clean(runtimeRoot),
		TokenFile:           filepath.Clean(tokenFile),
		LauncherTokenFile:   filepath.Clean(launcherTokenFile),
		BootstrapKeyFile:    filepath.Clean(bootstrapKeyFile),
		IngressPublicAddr:   ingressAddr,
		RuntimeAddr:         runtimeAddr,
		ManagerAddr:         managerAddr,
		IngressControlAddr:  ingressControlAddr,
		RuntimeCPAAddr:      cpaAddr,
		LauncherControlAddr: launcherControlAddr,
	}, nil
}

func resolveNativeIngressAddr(envMap map[string]string) (string, error) {
	raw := firstNonEmptyString(
		strings.TrimSpace(envMap["CPAMP_INGRESS_ADDR"]),
		strings.TrimSpace(envMap["HTTP_ADDR"]),
	)
	if raw == "" {
		port := firstNonEmptyString(
			strings.TrimSpace(envMap["PORT"]),
			strings.TrimSpace(envMap["CPAMP_PUBLIC_PORT"]),
		)
		if port != "" {
			raw = "0.0.0.0:" + port
		} else {
			raw = DefaultNativeIngressPublicAddr
		}
	}
	if strings.HasPrefix(raw, ":") {
		raw = "0.0.0.0" + raw
	}
	host, portStr, err := net.SplitHostPort(raw)
	if err != nil {
		return "", fmt.Errorf("invalid ingress address %q: %w", raw, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid ingress port in %q", raw)
	}
	if host == "" {
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func resolveNativeLoopbackServiceAddr(name, explicitAddr, explicitPort, urlOverride, defaultAddr string) (string, error) {
	candidate := explicitAddr
	if candidate == "" && explicitPort != "" {
		candidate = "127.0.0.1:" + explicitPort
	}
	if candidate == "" && urlOverride != "" {
		u, err := url.Parse(urlOverride)
		if err != nil || !strings.EqualFold(u.Scheme, "http") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
			return "", fmt.Errorf("invalid %s URL override %q", name, urlOverride)
		}
		candidate = u.Host
	}
	if candidate == "" {
		candidate = defaultAddr
	}
	host, portStr, err := net.SplitHostPort(candidate)
	if err != nil {
		return "", fmt.Errorf("invalid %s address %q: %w", name, candidate, err)
	}
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() || ip.Zone() != "" {
		return "", fmt.Errorf("%s address %q must bind a loopback IP", name, candidate)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid %s port in %q", name, candidate)
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(port)), nil
}

func checkDistinctPorts(namedAddrs map[string]string) error {
	seen := make(map[string]string, len(namedAddrs))
	for role, addr := range namedAddrs {
		_, port, err := net.SplitHostPort(addr)
		if err != nil || port == "0" {
			continue
		}
		if prevRole, dup := seen[port]; dup {
			return fmt.Errorf("port collision on %s between %s and %s", port, prevRole, role)
		}
		seen[port] = role
	}
	return nil
}

func checkNativeUpgradeSwapLock(homeDir string) error {
	lockDir := filepath.Join(homeDir, ".upgrade.lock")
	swappingFile := filepath.Join(lockDir, "swapping")
	if _, err := os.Lstat(swappingFile); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("refusing to launch while upgrade swap lock cannot be inspected: %w", err)
	}
	lockSt, err := os.Lstat(lockDir)
	if err != nil || lockSt.Mode()&fs.ModeSymlink != 0 || !lockSt.IsDir() {
		return fmt.Errorf("refusing to launch while an upgrade directory swap is active (%s)", swappingFile)
	}
	swapSt, err := os.Lstat(swappingFile)
	if err != nil || swapSt.Mode()&fs.ModeSymlink != 0 || !swapSt.Mode().IsRegular() {
		return fmt.Errorf("refusing to launch while an upgrade directory swap is active (%s)", swappingFile)
	}
	pidFile := filepath.Join(lockDir, "pid")
	pidSt, err := os.Lstat(pidFile)
	if err != nil || pidSt.Mode()&fs.ModeSymlink != 0 || !pidSt.Mode().IsRegular() {
		return fmt.Errorf("refusing to launch while an upgrade directory swap is active (%s)", swappingFile)
	}
	rawPID, err := os.ReadFile(pidFile)
	if err != nil {
		return fmt.Errorf("refusing to launch while an upgrade directory swap is active (%s)", swappingFile)
	}
	pidText := strings.TrimSpace(strings.SplitN(string(rawPID), "\n", 2)[0])
	pid, convErr := strconv.Atoi(pidText)
	if convErr != nil || pid <= 0 || !isDecimalDigits(pidText) {
		return fmt.Errorf("refusing to launch while an upgrade directory swap is active (%s)", swappingFile)
	}
	if err := unix.Kill(pid, 0); err == nil || errors.Is(err, unix.EPERM) {
		return fmt.Errorf("refusing to launch while an upgrade directory swap is active (pid %d)", pid)
	}
	return nil
}

func resolveNativeCPAIdentity(envMap map[string]string, allowNonRoot bool) (int, int, error) {
	uidRaw := strings.TrimSpace(envMap["CPAMP_RUNTIME_CPA_UID"])
	gidRaw := strings.TrimSpace(envMap["CPAMP_RUNTIME_CPA_GID"])
	if uidRaw != "" || gidRaw != "" {
		if uidRaw == "" || gidRaw == "" || !isDecimalDigits(uidRaw) || !isDecimalDigits(gidRaw) {
			return 0, 0, errors.New("CPAMP_RUNTIME_CPA_UID and CPAMP_RUNTIME_CPA_GID must both be positive decimal integers")
		}
		uid, errU := strconv.Atoi(uidRaw)
		gid, errG := strconv.Atoi(gidRaw)
		if errU != nil || errG != nil || uid <= 0 || gid <= 0 {
			return 0, 0, errors.New("CPAMP_RUNTIME_CPA_UID and CPAMP_RUNTIME_CPA_GID must be > 0")
		}
		return uid, gid, nil
	}

	if allowNonRoot && os.Geteuid() != RootUID {
		return os.Geteuid(), os.Getegid(), nil
	}

	cpaUser := firstNonEmptyString(strings.TrimSpace(envMap["CPAMP_CPA_USER"]), CPAUserName)
	if passwdBytes, err := os.ReadFile(DefaultPasswdPath); err == nil {
		if uid, gid, ok := lookupPasswdAccount(passwdBytes, cpaUser); ok && uid > 0 && gid > 0 {
			return uid, gid, nil
		}
	}
	return CPAUID, CPAGID, nil
}

func runNativeLauncher(opts LauncherCLIOptions, args []string, environ []string, hooks launcherRuntimeHooks) error {
	if err := checkNativeUpgradeSwapLock(opts.HomeDir); err != nil {
		return err
	}
	envMap := environSliceToMap(environ)
	getenv := func(k string) string { return envMap[k] }

	expectedUID := RootUID
	checkRootGID := true
	if hooks.allowNonRoot && os.Geteuid() != RootUID {
		expectedUID = os.Geteuid()
		checkRootGID = false
	}

	paths, err := resolveNativePathsAndAddrs(opts.HomeDir, envMap)
	if err != nil {
		return err
	}

	baseInfo, err := ResolveBaseInstallation(opts, expectedUID, getenv)
	if err != nil {
		return err
	}

	layout := ResolveRuntimeLayout(paths.DataDir, paths.RuntimeRoot, paths.TokenFile)
	layout.JournalPath = paths.JournalPath
	layout.SupervisorDir = filepath.Dir(paths.JournalPath)
	layout.ArtifactRoot = filepath.Join(layout.SupervisorDir, "artifacts")
	layout.ArtifactCPARoot = filepath.Join(layout.ArtifactRoot, "cpa")
	layout.LauncherTokenFile = paths.LauncherTokenFile

	store := NewSlotStoreWithOwner(layout.CPAMPRoot, layout.SlotsDir, layout.SlotStatePath, baseInfo.BaseEpoch, expectedUID, checkRootGID)

	plan, err := ArbitrateColdStart(ColdStartArbitrationInput{
		Base:        baseInfo,
		Store:       store,
		DataDir:     paths.DataDir,
		ManagerUID:  expectedUID,
		ManagerGID:  os.Getegid(),
		Args:        args,
		Environ:     environ,
		LogOut:      os.Stderr,
	})
	if err != nil {
		return err
	}
	if plan.ExecTarget != nil {
		return ExecuteProcessReplacement(plan.ExecTarget)
	}

	rollbackPreSupervision := func(failureCode string, cause error, ctrlSrv *SlotControlServer) error {
		if ctrlSrv != nil {
			ctrlCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = ctrlSrv.Shutdown(ctrlCtx)
			cancel()
		}
		if !plan.TrialHold {
			return cause
		}
		return rollbackPreSupervisionTrialFailure(
			plan,
			paths.DataDir,
			expectedUID,
			os.Getegid(),
			args,
			environ,
			failureCode,
			cause,
		)
	}

	cpaUID, cpaGID, err := resolveNativeCPAIdentity(envMap, hooks.allowNonRoot)
	if err != nil {
		return rollbackPreSupervision(FailureCodeSupervisorStartupFailed, err, nil)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	stopNativeOrphanedCPAProcesses(paths.RunDir, paths.RuntimeCPAAddr, plan.CPAExecutable, layout.GatewayDir, layout.ArtifactCPARoot, cpaUID)
	if err := checkNativePortsAvailable(paths); err != nil {
		return rollbackPreSupervision(FailureCodeSupervisorStartupFailed, err, nil)
	}


	prepared, err := prepareNativeFilesystem(ctx, paths, layout, plan, cpaUID, cpaGID, envMap)
	if err != nil {
		return rollbackPreSupervision(FailureCodeSupervisorStartupFailed, err, nil)
	}

	timings := TimingConfig{
		TrialWatchdogTimeout:     hooks.trialTimeout,
		TrialStabilizationWindow: hooks.trialStabilizationWindow,
	}.withDefaults(getenv)

	ctrlSrv, err := StartSlotControlServer(SlotControlConfig{
		ListenAddr:        paths.LauncherControlAddr,
		Mode:              LauncherModeNative,
		LauncherToken:     prepared.LauncherToken,
		Base:              plan.Base,
		RunningSlot:       plan.ActiveSlot,
		DataDir:           paths.DataDir,
		ManagerUID:        expectedUID,
		TrialTimeout:      timings.TrialWatchdogTimeout,
		Store:             store,
		ArchiveAddrPolicy: hooks.archiveAddrPolicy,
		ArchiveTLSRootCAs: hooks.archiveTLSRootCAs,
	})
	if err != nil {
		return rollbackPreSupervision(FailureCodeSupervisorStartupFailed, err, nil)
	}
	prepared.LauncherControlAddr = ctrlSrv.BoundAddr()
	// Write native run/ coordination files so cpa-manager-plusctl status/stop/upgrade stays 100% compatible.
	removeNativeChildStateFiles(paths.RunDir)
	_ = writeAtomicPrivateFile(filepath.Join(paths.RunDir, "cpa.address"), []byte(paths.RuntimeCPAAddr+"\n"), 0o600)
	_ = writeAtomicPrivateFile(filepath.Join(paths.RunDir, "launcher.drain"), []byte(fmt.Sprintf("%d\n", int(timings.DrainTimeout.Seconds()))), 0o600)
	_ = writeNativePIDRecord(paths.PIDFile, os.Getpid())

	var (
		pidsMu    sync.Mutex
		childPIDs = make(map[string]int, 3)
	)
	provStopCh := make(chan struct{})
	var provStopOnce sync.Once
	stopProvenanceLoop := func() {
		provStopOnce.Do(func() { close(provStopCh) })
	}
	defer stopProvenanceLoop()

	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-provStopCh:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				pidsMu.Lock()
				supPID := childPIDs["supervisor"]
				pidsMu.Unlock()
				refreshNativeCPAProvenance(paths.RunDir, os.Getpid(), supPID, cpaUID)
			}
		}
	}()

	supProbeClient := newLoopbackNoProxyHTTPClient(max(timings.ReadinessPollInterval*2, 500*time.Millisecond))
	supCfg := SupervisorConfig{
		SupervisorSpec: ChildSpec{
			Name:       "supervisor",
			Prefix:     "[supervisor] ",
			Executable: plan.SupervisorExecutable,
			Dir:        layout.GatewayDir,
			Env:        BuildSupervisorChildEnv(prepared, envMap),
			UID:        expectedUID,
			GID:        os.Getegid(),
			ClearSupp:  false,
		},
		ManagerSpec: ChildSpec{
			Name:       "manager",
			Prefix:     "[manager] ",
			Executable: plan.ManagerExecutable,
			Dir:        paths.DataDir,
			Env: BuildManagerChildEnvWithAddrs(
				prepared,
				paths.ManagerAddr,
				paths.RuntimeAddr,
				paths.IngressControlAddr,
				envMap,
			),
			UID:       expectedUID,
			GID:       os.Getegid(),
			ClearSupp: false,
			HasUmask:  true,
			Umask:     0o077,
		},
		IngressSpec: ChildSpec{
			Name:       "ingress",
			Prefix:     "[ingress] ",
			Executable: plan.IngressExecutable,
			Dir:        paths.HomeDir,
			Env: BuildIngressChildEnvWithAddrs(
				timings.DrainTimeout,
				paths.IngressPublicAddr,
				paths.ManagerAddr,
				paths.RuntimeCPAAddr,
				paths.IngressControlAddr,
				envMap,
			),
			UID:       expectedUID,
			GID:       os.Getegid(),
			ClearSupp: false,
		},
		SupervisorTCPAddr:    paths.RuntimeAddr,
		SupervisorReadyFunc: func(checkCtx context.Context) bool {
			pidsMu.Lock()
			supPID := childPIDs["supervisor"]
			pidsMu.Unlock()
			if supPID <= 0 {
				return false
			}
			if runtime.GOOS == "linux" && !procPIDListensOnTCPAddr("/proc", supPID, paths.RuntimeAddr) {
				return false
			}
			return probeRuntimeSupervisorHTTPReady(checkCtx, supProbeClient, paths.RuntimeAddr, prepared.RuntimeToken)
		},
		ManagerHealthURL:     "http://" + paths.ManagerAddr + "/health",
		IngressPublicTCPAddr: NormalizeTCPProbeAddr(paths.IngressPublicAddr),
		IngressAdminTCPAddr:  paths.IngressControlAddr,
		Timings:              timings,
		Stdout:               os.Stdout,
		Stderr:               os.Stderr,
		SetCredentials:       false,
		SlotControl:          ctrlSrv,
		SlotStore:            store,
		BaseInstallRoot:      plan.Base.InstallRoot,
		BaseVersion:          plan.Base.BaseVersion,
		ActiveSlot:           plan.ActiveSlot,
		LauncherArgs:         args,
		BaseEnviron:          environ,
		OnChildStart: func(name string, pid int) {
			pidsMu.Lock()
			childPIDs[name] = pid
			pidsMu.Unlock()
			switch name {
			case "supervisor":
				_ = writeNativePIDRecord(filepath.Join(paths.RunDir, "runtime-supervisor.pid"), pid)
				refreshNativeCPAProvenance(paths.RunDir, os.Getpid(), pid, cpaUID)
			case "manager":
				_ = writeNativePIDRecord(filepath.Join(paths.RunDir, "manager.pid"), pid)
			case "ingress":
				_ = writeNativePIDRecord(filepath.Join(paths.RunDir, "ingress.pid"), pid)
			}
		},
		OnAllReady: func() {
			pidsMu.Lock()
			supPID := childPIDs["supervisor"]
			mgrPID := childPIDs["manager"]
			ingPID := childPIDs["ingress"]
			pidsMu.Unlock()
			readyBody := fmt.Sprintf(
				"pid=%d\nruntime_supervisor_pid=%d\nmanager_pid=%d\ningress_pid=%d\n",
				os.Getpid(),
				supPID,
				mgrPID,
				ingPID,
			)
			_ = writeAtomicPrivateFile(filepath.Join(paths.RunDir, "launcher.ready"), []byte(readyBody), 0o600)
			refreshNativeCPAProvenance(paths.RunDir, os.Getpid(), supPID, cpaUID)
		},
		OnBeforeExec: func() {
			stopProvenanceLoop()
			removeNativeChildStateFiles(paths.RunDir)
			stopNativeOrphanedCPAProcesses(paths.RunDir, paths.RuntimeCPAAddr, plan.CPAExecutable, layout.GatewayDir, layout.ArtifactCPARoot, cpaUID)
		},
	}

	if plan.TrialHold {
		supCfg.Trial = &TrialWatchdogConfig{
			Store:               store,
			ActiveSlot:          plan.ActiveSlot,
			LauncherToken:       prepared.LauncherToken,
			ManagerAddr:         paths.ManagerAddr,
			DataDir:             paths.DataDir,
			ManagerUID:          expectedUID,
			ManagerGID:          os.Getegid(),
			RollbackLauncherExe: plan.RollbackLauncherExe,
			RollbackManagerExe:  plan.RollbackManagerExe,
			BaseInstallRoot:     plan.Base.InstallRoot,
			BaseVersion:         plan.Base.BaseVersion,
			LauncherArgs:        args,
			BaseEnviron:         environ,
		}
	}

	if err := configureUpgradeWorker(ctx, &supCfg); err != nil {
		stopProvenanceLoop()
		removeNativeChildStateFiles(paths.RunDir)
		_ = os.Remove(filepath.Join(paths.RunDir, "launcher.drain"))
		_ = os.Remove(paths.PIDFile)
		return rollbackPreSupervision(FailureCodeManagerStartupFailed, err, ctrlSrv)
	}

	tr, err := RunSupervisorWithSlotTransition(ctx, supCfg)
	stopProvenanceLoop()
	stop()
	if tr != nil {
		return ExecuteProcessReplacement(tr)
	}

	removeNativeChildStateFiles(paths.RunDir)
	_ = os.Remove(filepath.Join(paths.RunDir, "launcher.drain"))
	stopNativeOrphanedCPAProcesses(paths.RunDir, paths.RuntimeCPAAddr, plan.CPAExecutable, layout.GatewayDir, layout.ArtifactCPARoot, cpaUID)
	_ = os.Remove(paths.PIDFile)
	return err
}

func prepareNativeFilesystem(
	ctx context.Context,
	paths nativePathsAndAddrs,
	layout RuntimeLayout,
	plan *ArbitratedExecutionPlan,
	cpaUID, cpaGID int,
	envMap map[string]string,
) (*PreparedRuntime, error) {
	privateDirs := []string{
		paths.DataDir,
		paths.RunDir,
		paths.LogDir,
		filepath.Dir(paths.PIDFile),
		filepath.Dir(paths.LogFile),
		paths.RuntimeRoot,
		layout.SupervisorDir,
		layout.ArtifactRoot,
		layout.ArtifactCPARoot,
		filepath.Join(layout.SupervisorDir, "active"),
		filepath.Join(layout.SupervisorDir, "active", "cpa"),
		layout.GatewayDir,
		layout.GatewayAuthDir,
		layout.GatewayLogsDir,
		layout.GatewayPluginsDir,
		layout.TokenDir,
	}
	for _, d := range privateDirs {
		if err := ensureNativeNonSymlinkDir(d, 0o700); err != nil {
			return nil, err
		}
	}
	for _, slotDir := range []string{layout.CPAMPRoot, layout.SlotsDir} {
		if err := ensureNativeNonSymlinkDir(slotDir, 0o755); err != nil {
			return nil, err
		}
		if os.Geteuid() == RootUID {
			_ = os.Chown(slotDir, RootUID, RootGID)
		}
		_ = os.Chmod(slotDir, 0o755)
	}

	explicitToken := strings.TrimSpace(envMap["CPAMP_RUNTIME_TOKEN"])
	runtimeToken, err := prepareNativeSecretToken(paths.TokenFile, explicitToken, os.Geteuid())
	if err != nil {
		return nil, err
	}
	launcherToken, err := prepareNativeSecretToken(paths.LauncherTokenFile, "", os.Geteuid())
	if err != nil {
		return nil, err
	}

	if err := seedNativeGatewayConfig(layout.GatewayConfigPath, plan.SeedConfigPath, layout.GatewayDir, paths.RuntimeCPAAddr, paths.BootstrapKeyFile); err != nil {
		return nil, err
	}

	// Strip group/other permissions on top-level regular Manager files in DataDir before opening
	// 0:cpaGID 0710 directory traversal so unprivileged CPA processes cannot read Manager files.
	if err := tightenDataDirTopLevelRegularFiles(paths.DataDir, os.Geteuid() == RootUID); err != nil {
		return nil, err
	}

	rootPrivateSupervisorDirs := []string{
		filepath.Join(layout.SupervisorDir, "active"),
		filepath.Join(layout.SupervisorDir, "active", "cpa"),
	}
	for _, pd := range rootPrivateSupervisorDirs {
		if err := setNativeDirOwnerAndPermNoFollow(pd, RootUID, RootGID, 0o700, os.Geteuid() == RootUID); err != nil {
			return nil, err
		}
	}

	// Set 0:cpaGID 0710 traversal permissions on corridor directories while keeping Manager files and supervisor/active root-private.
	if os.Geteuid() == RootUID {
		traverseDirs := []string{
			paths.DataDir,
			paths.RuntimeRoot,
			layout.SupervisorDir,
			layout.ArtifactRoot,
			layout.ArtifactCPARoot,
		}
		for _, td := range traverseDirs {
			if err := setNativeDirOwnerAndPermNoFollow(td, RootUID, cpaGID, 0o710, true); err != nil {
				return nil, err
			}
		}
		if err := repairGatewayTreeNoFollowFD(ctx, layout, cpaUID, cpaGID); err != nil {
			return nil, err
		}
	}
	manifestPath := plan.CPAArtifactManifestPath
	if envManifest := strings.TrimSpace(envMap["CPAMP_CPA_ARTIFACT_MANIFEST"]); envManifest != "" {
		manifestPath = envManifest
	}
	runtimeIdentity := firstNonEmptyString(strings.TrimSpace(envMap["CPAMP_RUNTIME_IDENTITY"]), DefaultRuntimeIdentity)

	return &PreparedRuntime{
		Layout:                  layout,
		DataDir:                 paths.DataDir,
		RuntimeRoot:             paths.RuntimeRoot,
		SupervisorDir:           layout.SupervisorDir,
		ArtifactRoot:            layout.ArtifactRoot,
		ArtifactCPARoot:         layout.ArtifactCPARoot,
		JournalPath:             paths.JournalPath,
		GatewayDir:              layout.GatewayDir,
		GatewayAuthDir:          layout.GatewayAuthDir,
		GatewayLogsDir:          layout.GatewayLogsDir,
		GatewayPluginsDir:       layout.GatewayPluginsDir,
		GatewayConfigPath:       layout.GatewayConfigPath,
		TokenDir:                layout.TokenDir,
		TokenFile:               paths.TokenFile,
		RuntimeToken:            runtimeToken,
		LauncherTokenFile:       paths.LauncherTokenFile,
		LauncherToken:           launcherToken,
		CPAUID:                  cpaUID,
		CPAGID:                  cpaGID,
		ManagerUID:              os.Geteuid(),
		ManagerGID:              os.Getegid(),
		IngressUID:              os.Geteuid(),
		IngressGID:              os.Getegid(),
		RuntimeAddr:             paths.RuntimeAddr,
		RuntimeIdentity:         runtimeIdentity,
		CPAExecutablePath:       plan.CPAExecutable,
		CPAArtifactManifestPath: manifestPath,
		RuntimeCPAAddr:          paths.RuntimeCPAAddr,
		LauncherControlAddr:     paths.LauncherControlAddr,
		BaseInstallRoot:         plan.Base.InstallRoot,
		BaseVersion:             plan.Base.BaseVersion,
		RunningSlot:             plan.ActiveSlot,
		TrialHold:               plan.TrialHold,
	}, nil
}


func setNativeDirOwnerAndPermNoFollow(dir string, uid, gid int, perm uint32, chown bool) error {
	fd, _, err := openNoFollowDirFD(dir)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if chown {
		if err := unix.Fchown(fd, uid, gid); err != nil {
			return err
		}
	}
	return unix.Fchmod(fd, perm)
}

func tightenDataDirTopLevelRegularFiles(dataDir string, setRootOwner bool) error {
	dirFD, _, err := openNoFollowDirFD(dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dirFD) }()

	dupFD, err := unix.Dup(dirFD)
	if err != nil {
		return err
	}
	if _, fcntlErr := unix.FcntlInt(uintptr(dupFD), unix.F_SETFD, unix.FD_CLOEXEC); fcntlErr != nil {
		_ = unix.Close(dupFD)
		return fcntlErr
	}
	dirFile := os.NewFile(uintptr(dupFD), dataDir)
	entries, err := dirFile.ReadDir(-1)
	_ = dirFile.Close()
	if err != nil {
		return err
	}

	for _, entry := range entries {
		name := entry.Name()
		var st unix.Stat_t
		if err := unix.Fstatat(dirFD, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return err
		}
		if (uint32(st.Mode) & unix.S_IFMT) != unix.S_IFREG {
			continue
		}
		fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return err
		}
		var fst unix.Stat_t
		if err := unix.Fstat(fd, &fst); err != nil {
			_ = unix.Close(fd)
			return err
		}
		if (uint32(fst.Mode) & unix.S_IFMT) == unix.S_IFREG {
			if setRootOwner && (int(fst.Uid) != RootUID || int(fst.Gid) != RootGID) {
				if err := unix.Fchown(fd, RootUID, RootGID); err != nil {
					_ = unix.Close(fd)
					return err
				}
			}
			perm := uint32(fst.Mode) & 0o7777
			tightened := perm &^ 0o077
			if perm != tightened {
				if err := unix.Fchmod(fd, tightened); err != nil {
					_ = unix.Close(fd)
					return err
				}
			}
		}
		_ = unix.Close(fd)
	}
	return nil
}
func ensureNativeNonSymlinkDir(dir string, mode fs.FileMode) error {
	clean := filepath.Clean(dir)
	st, err := os.Lstat(clean)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(clean, mode); err != nil {
			return err
		}
		st, err = os.Lstat(clean)
		if err != nil {
			return err
		}
	}
	if st.Mode()&fs.ModeSymlink != 0 || !st.IsDir() {
		return fmt.Errorf("refusing non-directory or symlinked runtime path: %s", clean)
	}
	return nil
}

func prepareNativeSecretToken(tokenFile, explicitToken string, expectedOwnerUID int) (string, error) {
	var tok string
	parentFD, _, dirErr := openNoFollowDirFD(filepath.Dir(tokenFile))
	if dirErr != nil {
		return "", dirErr
	}
	raw, st, readErr := readRegularFileAtNoFollow(parentFD, filepath.Base(tokenFile), 64*1024)
	_ = unix.Close(parentFD)
	if readErr == nil {
		if int(st.Uid) != expectedOwnerUID {
			return "", fmt.Errorf("secret token file %s owner uid %d != expected %d", tokenFile, st.Uid, expectedOwnerUID)
		}
		tok = string(raw)
		if err := validateRuntimeToken(tok); err != nil {
			return "", err
		}
		if explicitToken != "" && tok != explicitToken {
			return "", errors.New("Runtime transport secret mismatch")
		}
	} else if !errors.Is(readErr, unix.ENOENT) {
		return "", readErr
	}

	if tok == "" {
		candidate := explicitToken
		if candidate == "" {
			var buf [32]byte
			if _, err := rand.Read(buf[:]); err != nil {
				return "", err
			}
			candidate = hex.EncodeToString(buf[:])
		} else if err := validateRuntimeToken(candidate); err != nil {
			return "", err
		}
		if err := writeAtomicPrivateFile(tokenFile, []byte(candidate), 0o600); err != nil {
			return "", err
		}
		tok = candidate
	}
	if os.Geteuid() == RootUID {
		_ = os.Chown(tokenFile, RootUID, RootGID)
	}
	_ = os.Chmod(tokenFile, 0o600)
	return tok, nil
}

var (
	reYAMLPort        = regexp.MustCompile(`(?m)^port:\s*8317\s*$`)
	reYAMLHost        = regexp.MustCompile(`(?m)^host:\s*""\s*$`)
	reYAMLAuthDir     = regexp.MustCompile(`(?m)^auth-dir:\s*"/runtime/gateway/auth"\s*$`)
	reYAMLPluginDir   = regexp.MustCompile(`(?m)^(\s*)plugin-dir:\s*"/runtime/gateway/plugins"\s*$`)
	reYAMLEmptyAPIKey = regexp.MustCompile(`(?m)^api-keys:\s*\[\]\s*$`)
)

func seedNativeGatewayConfig(configPath, seedPath, gatewayDir, cpaAddr, bootstrapKeyFile string) error {
	if st, err := os.Lstat(configPath); err == nil {
		if st.Mode()&fs.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return fmt.Errorf("refusing non-regular gateway config %s", configPath)
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	seedRaw, err := os.ReadFile(seedPath)
	if err != nil {
		return fmt.Errorf("failed reading seed config %s: %w", seedPath, err)
	}

	bootstrapKey, err := ensureNativeBootstrapKey(bootstrapKeyFile)
	if err != nil {
		return err
	}

	cpaHost, cpaPort, err := net.SplitHostPort(cpaAddr)
	if err != nil {
		return err
	}

	escapeYAMLQuoted := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		return `"` + s + `"`
	}

	rendered := string(seedRaw)
	rendered = reYAMLPort.ReplaceAllString(rendered, "port: "+cpaPort)
	rendered = reYAMLHost.ReplaceAllString(rendered, "host: "+escapeYAMLQuoted(cpaHost))
	rendered = reYAMLAuthDir.ReplaceAllString(rendered, "auth-dir: "+escapeYAMLQuoted(filepath.Join(gatewayDir, "auth")))
	rendered = reYAMLPluginDir.ReplaceAllString(rendered, "${1}plugin-dir: "+escapeYAMLQuoted(filepath.Join(gatewayDir, "plugins")))
	rendered = reYAMLEmptyAPIKey.ReplaceAllString(rendered, "api-keys:\n  - "+escapeYAMLQuoted(bootstrapKey))

	return writeAtomicPrivateFile(configPath, []byte(rendered), 0o600)
}

func ensureNativeBootstrapKey(keyFile string) (string, error) {
	if st, err := os.Lstat(keyFile); err == nil {
		if st.Mode()&fs.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return "", fmt.Errorf("refusing non-regular bootstrap key file %s", keyFile)
		}
		raw, readErr := os.ReadFile(keyFile)
		if readErr == nil {
			trimmed := strings.TrimSpace(string(raw))
			if len(trimmed) >= 32 {
				return trimmed, nil
			}
		}
	}
	tok, err := randomHexToken(24)
	if err != nil {
		return "", err
	}
	key := "cpamp-bootstrap-" + tok
	if err := writeAtomicPrivateFile(keyFile, []byte(key+"\n"), 0o600); err != nil {
		return "", err
	}
	return key, nil
}

func writeAtomicPrivateFile(target string, data []byte, mode fs.FileMode) error {
	suffix, err := randomHexToken(6)
	if err != nil {
		return err
	}
	tmp := target + ".tmp." + suffix
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, wErr := f.Write(data)
	cErr := f.Chmod(mode)
	sErr := syncIgnoreEINVAL(int(f.Fd()))
	clErr := f.Close()
	if err := errors.Join(wErr, cErr, sErr, clErr); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func writeNativePIDRecord(targetPIDFile string, pid int) error {
	if pid <= 0 {
		return nil
	}
	startMarker := queryProcessStartMarker(pid)
	exePath := queryProcessExecutable(pid)
	cmdLine := queryProcessCommandLine(pid)
	if startMarker == "" {
		return nil
	}
	body := fmt.Sprintf("pid=%d\nstart=%s\nbinary=%s\ncommand=%s\n", pid, startMarker, exePath, cmdLine)
	return writeAtomicPrivateFile(targetPIDFile, []byte(body), 0o600)
}

func queryProcessStartMarker(pid int) string {
	statPath := fmt.Sprintf("/proc/%d/stat", pid)
	if raw, err := os.ReadFile(statPath); err == nil {
		s := strings.TrimSpace(string(raw))
		// /proc/<pid>/stat has (comm) in field 2 which may contain spaces; slice past the last ')'.
		if rparen := strings.LastIndexByte(s, ')'); rparen >= 0 && rparen+2 < len(s) {
			rest := strings.Fields(s[rparen+2:])
			// Field 3 is rest[0], so field 22 is rest[19].
			if len(rest) >= 20 {
				return rest[19]
			}
		}
	}
	out, err := exec.Command("ps", "-ww", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(string(out)), " ")
}

func queryProcessExecutable(pid int) string {
	procExe := fmt.Sprintf("/proc/%d/exe", pid)
	if resolved, err := filepath.EvalSymlinks(procExe); err == nil && resolved != "" {
		return resolved
	}
	if pid == os.Getpid() {
		if selfExe, err := os.Executable(); err == nil {
			if resolved, evalErr := filepath.EvalSymlinks(selfExe); evalErr == nil {
				return resolved
			}
			return selfExe
		}
	}
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "txt", "-Fn").Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "n/") {
				return strings.TrimPrefix(line, "n")
			}
		}
	}
	return ""
}

func queryProcessCommandLine(pid int) string {
	out, err := exec.Command("ps", "-ww", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func removeNativeChildStateFiles(runDir string) {
	for _, name := range []string{
		"launcher.ready",
		"runtime-supervisor.pid",
		"manager.pid",
		"ingress.pid",
	} {
		_ = os.Remove(filepath.Join(runDir, name))
	}
}

func refreshNativeCPAProvenance(runDir string, launcherPID, supPID, cpaUID int) {
	if supPID <= 0 {
		return
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	var lines []string
	for _, e := range entries {
		name := e.Name()
		if !isDecimalDigits(name) {
			continue
		}
		pid, convErr := strconv.Atoi(name)
		if convErr != nil || pid <= 1 || pid == launcherPID || pid == supPID {
			continue
		}
		ppid, uid, ok := readProcPPIDAndUID(pid)
		if !ok || ppid != supPID || uid != cpaUID {
			continue
		}
		startMarker := queryProcessStartMarker(pid)
		exePath := queryProcessExecutable(pid)
		if startMarker != "" {
			lines = append(lines, fmt.Sprintf("%d|%s|%s", pid, startMarker, exePath))
		}
	}
	if len(lines) > 0 {
		_ = writeAtomicPrivateFile(filepath.Join(runDir, "cpa-instance.provenance"), []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	}
}

func readProcPPIDAndUID(pid int) (int, int, bool) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, 0, false
	}
	var (
		ppid    = -1
		uid     = -1
		hasPPID bool
		hasUID  bool
	)
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "PPid:\t") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if v, err := strconv.Atoi(fields[1]); err == nil {
					ppid = v
					hasPPID = true
				}
			}
		} else if strings.HasPrefix(line, "Uid:\t") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if v, err := strconv.Atoi(fields[1]); err == nil {
					uid = v
					hasUID = true
				}
			}
		}
	}
	return ppid, uid, hasPPID && hasUID
}

func stopNativeOrphanedCPAProcesses(runDir, cpaAddr, cpaBin, gatewayDir, artifactsCPADir string, cpaUID int) {
	provFile := filepath.Join(runDir, "cpa-instance.provenance")
	raw, err := os.ReadFile(provFile)
	if err != nil {
		return
	}
	var targets []int
	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(parts) < 2 || !isDecimalDigits(parts[0]) {
			continue
		}
		pid, convErr := strconv.Atoi(parts[0])
		if convErr != nil || pid <= 1 {
			continue
		}
		if queryProcessStartMarker(pid) != parts[1] {
			continue
		}
		if _, uid, ok := readProcPPIDAndUID(pid); ok && uid != cpaUID {
			continue
		}
		exe := queryProcessExecutable(pid)
		if exe != "" && exe != cpaBin && !isPathWithin(exe, artifactsCPADir) && !isPathWithin(exe, gatewayDir) {
			continue
		}
		targets = append(targets, pid)
	}
	for _, pid := range targets {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		alive := false
		for _, pid := range targets {
			if syscall.Kill(pid, 0) == nil {
				alive = true
				break
			}
		}
		if !alive {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, pid := range targets {
		if syscall.Kill(pid, 0) == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	_ = os.Remove(provFile)
	_ = cpaAddr
}

func checkNativePortsAvailable(paths nativePathsAndAddrs) error {
	checks := []struct {
		name string
		addr string
	}{
		{name: "runtime-supervisor", addr: paths.RuntimeAddr},
		{name: "manager", addr: paths.ManagerAddr},
		{name: "ingress", addr: paths.IngressPublicAddr},
		{name: "ingress-control", addr: paths.IngressControlAddr},
	}
	for _, item := range checks {
		probeAddr := NormalizeTCPProbeAddr(item.addr)
		if probeAddr == "" {
			continue
		}
		if tcpProbeReady(context.Background(), probeAddr, 125*time.Millisecond) {
			return fmt.Errorf("native %s address %s is already accepting TCP connections from an existing listener", item.name, item.addr)
		}
	}
	return nil
}

func newLoopbackNoProxyHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 500 * time.Millisecond
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func probeRuntimeSupervisorHTTPReady(ctx context.Context, client *http.Client, runtimeAddr, runtimeToken string) bool {
	if strings.TrimSpace(runtimeAddr) == "" || strings.TrimSpace(runtimeToken) == "" {
		return false
	}
	statusReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+runtimeAddr+"/v1/runtime/status", nil)
	if err != nil {
		return false
	}
	statusReq.Header.Set("Authorization", "Bearer "+runtimeToken)
	statusReq.Header.Set("X-CPAMP-Runtime-Features", "active-gateway-artifact-v1")
	resp, err := client.Do(statusReq)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return true
	}
	if resp.StatusCode != http.StatusNotFound {
		return false
	}

	healthReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+runtimeAddr+"/health", nil)
	if err != nil {
		return false
	}
	hResp, err := client.Do(healthReq)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(hResp.Body, 64*1024))
	_ = hResp.Body.Close()
	return hResp.StatusCode == http.StatusOK
}

// procPIDListensOnTCPAddr verifies on Linux /proc that pid owns a TCP_LISTEN (0A)
// socket matching runtimeAddr and that no foreign process owns another TCP_LISTEN
// socket capable of accepting connections to runtimeAddr.
func procPIDListensOnTCPAddr(procRoot string, pid int, runtimeAddr string) bool {
	if pid <= 0 {
		return false
	}
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(runtimeAddr))
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	targetIP, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	targetIP = targetIP.Unmap()
	portNum, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || portNum == 0 {
		return false
	}

	ownedInodes := readProcPIDSocketInodes(procRoot, pid)
	if len(ownedInodes) == 0 {
		return false
	}

	tables := []struct {
		path   string
		isIPv6 bool
	}{
		{path: filepath.Join(procRoot, "net", "tcp"), isIPv6: false},
		{path: filepath.Join(procRoot, "net", "tcp6"), isIPv6: true},
	}
	readableTables := 0
	matchedListeners := 0
	for _, tbl := range tables {
		raw, readErr := os.ReadFile(tbl.path)
		if readErr != nil {
			continue
		}
		readableTables++
		lines := strings.Split(string(raw), "\n")
		for idx, line := range lines {
			if idx == 0 {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 10 {
				continue
			}
			// st == 0A is TCP_LISTEN; inode 0 is an unhashed/closing socket placeholder.
			if !strings.EqualFold(fields[3], "0A") {
				continue
			}
			inode := fields[9]
			if inode == "" || inode == "0" {
				continue
			}
			hexIP, hexPort, ok := strings.Cut(fields[1], ":")
			if !ok {
				continue
			}
			listenPort, parseErr := strconv.ParseUint(hexPort, 16, 16)
			if parseErr != nil || uint16(listenPort) != uint16(portNum) {
				continue
			}
			listenIP, ipOK := parseProcNetHexIP(hexIP, tbl.isIPv6)
			if !ipOK {
				return false
			}
			if !procListenIPMatchesTarget(listenIP, targetIP) {
				continue
			}
			if _, ok := ownedInodes[inode]; !ok {
				return false
			}
			matchedListeners++
		}
	}
	return readableTables > 0 && matchedListeners > 0
}

func readProcPIDSocketInodes(procRoot string, pid int) map[string]struct{} {
	fdDir := filepath.Join(procRoot, strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return nil
	}
	inodes := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		target, linkErr := os.Readlink(filepath.Join(fdDir, entry.Name()))
		if linkErr != nil {
			continue
		}
		if strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
			inode := target[len("socket:[") : len(target)-1]
			if inode != "" && inode != "0" && isDecimalDigits(inode) {
				inodes[inode] = struct{}{}
			}
		}
	}
	return inodes
}

func parseProcNetHexIP(hexIP string, isIPv6 bool) (netip.Addr, bool) {
	return parseProcNetHexIPWithOrder(hexIP, isIPv6, binary.NativeEndian)
}

func parseProcNetHexIPWithOrder(hexIP string, isIPv6 bool, order binary.ByteOrder) (netip.Addr, bool) {
	if !isIPv6 {
		if len(hexIP) != 8 {
			return netip.Addr{}, false
		}
		v, err := strconv.ParseUint(hexIP, 16, 32)
		if err != nil {
			return netip.Addr{}, false
		}
		var b [4]byte
		order.PutUint32(b[:], uint32(v))
		return netip.AddrFrom4(b), true
	}
	if len(hexIP) != 32 {
		return netip.Addr{}, false
	}
	var b [16]byte
	for i := range 4 {
		v, err := strconv.ParseUint(hexIP[i*8:(i+1)*8], 16, 32)
		if err != nil {
			return netip.Addr{}, false
		}
		order.PutUint32(b[i*4:(i+1)*4], uint32(v))
	}
	return netip.AddrFrom16(b), true
}

func procListenIPMatchesTarget(listenIP, targetIP netip.Addr) bool {
	if listenIP.Unmap() == targetIP {
		return true
	}
	if listenIP.Is4() && listenIP.IsUnspecified() && targetIP.Is4() {
		return true
	}
	if listenIP.Is6() && listenIP.IsUnspecified() {
		return true
	}
	return false
}
