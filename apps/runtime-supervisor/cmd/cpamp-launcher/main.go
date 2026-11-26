//go:build unix

package main

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// launcherRuntimeHooks holds optional overrides strictly injected from TestMain
// for unprivileged cross-platform subprocess tests; zero-value in production.
type launcherRuntimeHooks struct {
	allowNonLinux            bool
	allowNonRoot             bool
	trialTimeout             time.Duration
	trialStabilizationWindow time.Duration
	archiveAddrPolicy        func(netip.Addr) bool
	archiveTLSRootCAs        *x509.CertPool
}

func main() {
	if err := runLauncherEntrypoint(os.Args, os.Environ(), launcherRuntimeHooks{}); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func printLauncherUsage() {
	fmt.Fprintln(os.Stdout, "Usage: cpamp-launcher [--mode=docker|native] [--home=<dir>]")
}

func runLauncherEntrypoint(args []string, environ []string, hooks launcherRuntimeHooks) error {
	envMap := environSliceToMap(environ)
	getenv := func(k string) string { return envMap[k] }

	var cliArgs []string
	if len(args) > 1 {
		cliArgs = args[1:]
	}
	opts, err := ParseLauncherCLIArgs(cliArgs, getenv)
	if err != nil {
		return err
	}
	if opts.ShowHelp {
		printLauncherUsage()
		return nil
	}
	if runtime.GOOS != "linux" && !hooks.allowNonLinux {
		return errors.New("cpamp-launcher is only supported on linux")
	}
	if os.Geteuid() != RootUID && !hooks.allowNonRoot {
		return fmt.Errorf("cpamp-launcher must run as root (euid 0, got %d)", os.Geteuid())
	}

	if opts.Mode == LauncherModeNative {
		return runNativeLauncher(opts, args, environ, hooks)
	}
	return runDockerLauncher(opts, args, environ, hooks)
}

func runDockerLauncher(opts LauncherCLIOptions, args []string, environ []string, hooks launcherRuntimeHooks) error {
	envMap := environSliceToMap(environ)
	getenv := func(k string) string { return envMap[k] }

	expectedUID := RootUID
	checkRootGID := true
	if hooks.allowNonRoot && os.Geteuid() != RootUID {
		expectedUID = os.Geteuid()
		checkRootGID = false
	}

	baseInfo, err := ResolveBaseInstallation(opts, expectedUID, getenv)
	if err != nil {
		return err
	}

	mountinfo, readErr := os.ReadFile(DefaultMountInfoPath)
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return fmt.Errorf("failed to read mountinfo %s: %w", DefaultMountInfoPath, readErr)
	}
	runtimeRoot := SelectRuntimeRoot(mountinfo)
	tokenFile := envOrDefaultFromMap(envMap, "CPAMP_RUNTIME_TOKEN_FILE", DefaultRuntimeTokenFile)
	layout := ResolveRuntimeLayout(DefaultDataDir, runtimeRoot, tokenFile)

	store := NewSlotStoreWithOwner(layout.CPAMPRoot, layout.SlotsDir, layout.SlotStatePath, baseInfo.BaseEpoch, expectedUID, checkRootGID)

	plan, err := ArbitrateColdStart(ColdStartArbitrationInput{
		Base:        baseInfo,
		Store:       store,
		DataDir:     layout.DataDir,
		ManagerUID:  ManagerUID,
		ManagerGID:  ManagerGID,
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

	rollbackPreSupervision := func(failureCode string, dataDir string, cause error, ctrlSrv *SlotControlServer) error {
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
			dataDir,
			expectedUID,
			ManagerGID,
			args,
			environ,
			failureCode,
			cause,
		)
	}

	controlAddr := envOrDefaultFromMap(envMap, "CPAMP_LAUNCHER_CONTROL_ADDR", DefaultLauncherControlAddr)
	if _, err := ValidateLoopbackListenAddr(controlAddr); err != nil {
		return rollbackPreSupervision(FailureCodeSupervisorStartupFailed, layout.DataDir, err, nil)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	prepCfg := PrepareConfigFromEnv()
	prepCfg.LauncherControlAddr = controlAddr
	prepCfg.BaseInstallRoot = plan.Base.InstallRoot
	prepCfg.BaseVersion = plan.Base.BaseVersion
	prepCfg.RunningSlot = plan.ActiveSlot
	prepCfg.TrialHold = plan.TrialHold
	if plan.ActiveSlot != "" {
		prepCfg.SeedConfigPath = plan.SeedConfigPath
		prepCfg.SeedLineagePath = plan.SeedLineagePath
		if !prepCfg.manifestFromEnv {
			prepCfg.CPAArtifactManifestPath = plan.CPAArtifactManifestPath
			prepCfg.trustedSlotRoot = plan.SlotRoot
		}
	}

	prepared, err := prepareContext(ctx, prepCfg, DefaultSystem())
	if err != nil {
		return rollbackPreSupervision(FailureCodeSupervisorStartupFailed, prepCfg.DataDir, err, nil)
	}

	timings := TimingConfig{
		TrialWatchdogTimeout:     hooks.trialTimeout,
		TrialStabilizationWindow: hooks.trialStabilizationWindow,
	}.withDefaults(getenv)

	ctrlSrv, err := StartSlotControlServer(SlotControlConfig{
		ListenAddr:        controlAddr,
		Mode:              LauncherModeDocker,
		LauncherToken:     prepared.LauncherToken,
		Base:              plan.Base,
		RunningSlot:       plan.ActiveSlot,
		DataDir:           prepared.DataDir,
		ManagerUID:        prepared.ManagerUID,
		TrialTimeout:      timings.TrialWatchdogTimeout,
		Store:             store,
		ArchiveAddrPolicy: hooks.archiveAddrPolicy,
		ArchiveTLSRootCAs: hooks.archiveTLSRootCAs,
	})
	if err != nil {
		return rollbackPreSupervision(FailureCodeSupervisorStartupFailed, prepared.DataDir, err, nil)
	}
	prepared.LauncherControlAddr = ctrlSrv.BoundAddr()

	supCfg := NewSupervisorConfig(prepared, environ)
	supCfg.Timings = timings
	supCfg.SupervisorSpec.Executable = plan.SupervisorExecutable
	supCfg.ManagerSpec.Executable = plan.ManagerExecutable
	supCfg.IngressSpec.Executable = plan.IngressExecutable
	supCfg.SlotControl = ctrlSrv
	supCfg.SlotStore = store
	supCfg.BaseInstallRoot = plan.Base.InstallRoot
	supCfg.BaseVersion = plan.Base.BaseVersion
	supCfg.ActiveSlot = plan.ActiveSlot
	supCfg.LauncherArgs = args
	supCfg.BaseEnviron = environ

	if plan.TrialHold {
		supCfg.Trial = &TrialWatchdogConfig{
			Store:               store,
			ActiveSlot:          plan.ActiveSlot,
			LauncherToken:       prepared.LauncherToken,
			ManagerAddr:         DefaultManagerAddr,
			DataDir:             prepared.DataDir,
			ManagerUID:          prepared.ManagerUID,
			ManagerGID:          prepared.ManagerGID,
			RollbackLauncherExe: plan.RollbackLauncherExe,
			RollbackManagerExe:  plan.RollbackManagerExe,
			BaseInstallRoot:     plan.Base.InstallRoot,
			BaseVersion:         plan.Base.BaseVersion,
			LauncherArgs:        args,
			BaseEnviron:         environ,
		}
	}

	if err := configureUpgradeWorker(ctx, &supCfg); err != nil {
		return rollbackPreSupervision(FailureCodeManagerStartupFailed, prepared.DataDir, err, ctrlSrv)
	}

	tr, err := RunSupervisorWithSlotTransition(ctx, supCfg)
	stop()
	if err != nil {
		return err
	}
	if tr != nil {
		return ExecuteProcessReplacement(tr)
	}
	return nil
}

func rollbackPreSupervisionTrialFailure(
	plan *ArbitratedExecutionPlan,
	dataDir string,
	managerUID, managerGID int,
	args, environ []string,
	failureCode string,
	cause error,
) error {
	_, _ = fmt.Fprintf(
		os.Stderr,
		"[launcher] pre-supervision step failed during trial %s: %v\n",
		plan.ActiveSlot,
		cause,
	)
	rbCfg := TrialWatchdogConfig{
		Store:               plan.Store,
		ActiveSlot:          plan.ActiveSlot,
		DataDir:             dataDir,
		ManagerUID:          managerUID,
		ManagerGID:          managerGID,
		RollbackLauncherExe: plan.RollbackLauncherExe,
		RollbackManagerExe:  plan.RollbackManagerExe,
		BaseInstallRoot:     plan.Base.InstallRoot,
		BaseVersion:         plan.Base.BaseVersion,
		LauncherArgs:        args,
		BaseEnviron:         environ,
	}
	rbTr, _, rbErr := ExecuteTrialRollback(rbCfg, plan.State, failureCode, os.Stderr)
	if rbErr != nil {
		return fmt.Errorf("pre-supervision trial setup failed (%v) and rollback failed: %w", cause, rbErr)
	}
	if rbTr != nil {
		return ExecuteProcessReplacement(rbTr)
	}
	return cause
}

func envOrDefaultFromMap(env map[string]string, key, def string) string {
	if v := strings.TrimSpace(env[key]); v != "" {
		return v
	}
	return def
}
