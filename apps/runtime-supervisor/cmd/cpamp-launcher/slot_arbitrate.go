//go:build unix

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	LauncherModeDocker = "docker"
	LauncherModeNative = "native"
	DefaultNativeHome  = "/opt/cpa-manager-plus"
)

// LauncherCLIOptions holds parsed command-line flags for cpamp-launcher.
type LauncherCLIOptions struct {
	Mode     string
	HomeDir  string
	ShowHelp bool
}

// ParseLauncherCLIArgs parses --mode=docker|native and --home=<dir> flags.
func ParseLauncherCLIArgs(args []string, getenv func(string) string) (LauncherCLIOptions, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	opts := LauncherCLIOptions{
		Mode: LauncherModeDocker,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help" || arg == "help":
			opts.ShowHelp = true
		case arg == "run":
			// Allowed compatibility verb used by cpa-manager-plus-launcher.
		case arg == "--mode":
			if i+1 >= len(args) {
				return LauncherCLIOptions{}, errors.New("--mode requires a value (docker|native)")
			}
			i++
			opts.Mode = strings.TrimSpace(args[i])
		case strings.HasPrefix(arg, "--mode="):
			opts.Mode = strings.TrimSpace(strings.TrimPrefix(arg, "--mode="))
		case arg == "--home":
			if i+1 >= len(args) {
				return LauncherCLIOptions{}, errors.New("--home requires a directory path")
			}
			i++
			opts.HomeDir = strings.TrimSpace(args[i])
		case strings.HasPrefix(arg, "--home="):
			opts.HomeDir = strings.TrimSpace(strings.TrimPrefix(arg, "--home="))
		default:
			return LauncherCLIOptions{}, fmt.Errorf("unsupported argument %q", arg)
		}
	}

	switch opts.Mode {
	case LauncherModeDocker, LauncherModeNative:
	default:
		return LauncherCLIOptions{}, fmt.Errorf("invalid --mode %q (want docker or native)", opts.Mode)
	}

	if opts.Mode == LauncherModeNative {
		if opts.HomeDir == "" {
			opts.HomeDir = strings.TrimSpace(getenv("CPAMP_NATIVE_HOME"))
		}
		if opts.HomeDir == "" {
			opts.HomeDir = DefaultNativeHome
		}
		absHome, err := filepath.Abs(opts.HomeDir)
		if err != nil {
			return LauncherCLIOptions{}, fmt.Errorf("invalid --home %q: %w", opts.HomeDir, err)
		}
		if resolved, evalErr := filepath.EvalSymlinks(absHome); evalErr == nil {
			absHome = resolved
		}
		opts.HomeDir = filepath.Clean(absHome)
	}
	return opts, nil
}

// BaseInstallationInfo describes the immutable base Docker image or Native installation root.
type BaseInstallationInfo struct {
	Mode                    string
	InstallRoot             string
	BaseVersion             string
	BaseEpoch               int
	Platform                string
	LauncherExecutable      string
	SupervisorExecutable    string
	ManagerExecutable       string
	IngressExecutable       string
	CPAExecutable           string
	SeedConfigPath          string
	SeedLineagePath         string
	CPAArtifactManifestPath string
}

type baseProvenanceDoc struct {
	Version string `json:"version"`
}

type baseABIDoc struct {
	SchemaVersion int    `json:"schema_version"`
	BaseEpoch     int    `json:"base_epoch"`
	Platform      string `json:"platform"`
}

// ResolveBaseInstallation resolves BaseVersion, BaseEpoch, and base binary/asset paths
// from the base Docker image ("/") or Native installation root ("<home>").
func ResolveBaseInstallation(opts LauncherCLIOptions, expectedOwnerUID int, getenv func(string) string) (BaseInstallationInfo, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	installRoot := strings.TrimSpace(getenv("CPAMP_BASE_INSTALL_ROOT"))
	if installRoot == "" {
		if opts.Mode == LauncherModeNative {
			installRoot = opts.HomeDir
		} else {
			installRoot = "/"
		}
	}
	cleanRoot := filepath.Clean(installRoot)
	if !filepath.IsAbs(cleanRoot) {
		return BaseInstallationInfo{}, fmt.Errorf("base install root %q must be an absolute path", installRoot)
	}
	if opts.Mode == LauncherModeNative && opts.HomeDir != "" && cleanRoot != filepath.Clean(opts.HomeDir) {
		return BaseInstallationInfo{}, fmt.Errorf("CPAMP_BASE_INSTALL_ROOT %q does not match native home %q", cleanRoot, opts.HomeDir)
	}

	rootFD, rootSt, err := openNoFollowDirFD(cleanRoot)
	if err != nil {
		return BaseInstallationInfo{}, fmt.Errorf("failed to open base install root %s: %w", cleanRoot, err)
	}
	_ = unix.Close(rootFD)
	if cleanRoot != "/" && int(rootSt.Uid) != expectedOwnerUID {
		return BaseInstallationInfo{}, fmt.Errorf("base install root %s owner uid %d != expected %d", cleanRoot, rootSt.Uid, expectedOwnerUID)
	}

	var shareCPAMP, binDir string
	if opts.Mode == LauncherModeDocker {
		usrLocalShare := filepath.Join(cleanRoot, "usr", "local", "share", "cpamp")
		altShare := filepath.Join(cleanRoot, "share", "cpamp")
		if cleanRoot != "/" {
			if _, statErr := os.Lstat(usrLocalShare); statErr != nil {
				if _, altErr := os.Lstat(altShare); altErr == nil {
					shareCPAMP = altShare
					binDir = filepath.Join(cleanRoot, "bin")
				}
			}
		}
		if shareCPAMP == "" {
			shareCPAMP = usrLocalShare
			binDir = filepath.Join(cleanRoot, "usr", "local", "bin")
		}
	} else {
		shareCPAMP = filepath.Join(cleanRoot, "share", "cpamp")
		binDir = strings.TrimSpace(getenv("CPAMP_NATIVE_BIN_DIR"))
		if binDir == "" {
			binDir = filepath.Join(cleanRoot, "bin")
		}
	}

	provenancePath := filepath.Join(shareCPAMP, "provenance.json")
	baseVersion, err := readBaseVersionFromProvenance(provenancePath, expectedOwnerUID, getenv)
	if err != nil {
		return BaseInstallationInfo{}, err
	}

	baseABIPath := filepath.Join(shareCPAMP, "release", "base-abi.json")
	baseEpoch, platform, err := readBaseABIFromFile(baseABIPath, expectedOwnerUID)
	if err != nil {
		return BaseInstallationInfo{}, err
	}

	info := BaseInstallationInfo{
		Mode:                    opts.Mode,
		InstallRoot:             cleanRoot,
		BaseVersion:             baseVersion,
		BaseEpoch:               baseEpoch,
		Platform:                platform,
		LauncherExecutable:      filepath.Join(binDir, "cpamp-launcher"),
		SeedConfigPath:          filepath.Join(shareCPAMP, "runtime", "config.seed.yaml"),
		SeedLineagePath:         filepath.Join(shareCPAMP, "runtime", "seed-lineage.json"),
		CPAArtifactManifestPath: filepath.Join(shareCPAMP, "runtime", "cpa-artifact.json"),
	}

	if opts.Mode == LauncherModeNative {
		info.SupervisorExecutable = firstNonEmptyString(
			strings.TrimSpace(getenv("CPAMP_NATIVE_SUPERVISOR_BIN")),
			resolvePreferredExecutable(binDir, "cpa-manager-plus-runtime-supervisor", "cpamp-runtime-supervisor"),
		)
		info.ManagerExecutable = firstNonEmptyString(
			strings.TrimSpace(getenv("CPAMP_NATIVE_MANAGER_BIN")),
			filepath.Join(binDir, "cpa-manager-plus"),
		)
		info.IngressExecutable = firstNonEmptyString(
			strings.TrimSpace(getenv("CPAMP_NATIVE_INGRESS_BIN")),
			resolvePreferredExecutable(binDir, "cpa-manager-plus-ingress", "cpamp-ingress"),
		)
		info.CPAExecutable = firstNonEmptyString(
			strings.TrimSpace(getenv("CPAMP_CPA_EXECUTABLE")),
			filepath.Join(binDir, "cli-proxy-api"),
		)
	} else {
		info.SupervisorExecutable = filepath.Join(binDir, "cpamp-runtime-supervisor")
		info.ManagerExecutable = filepath.Join(binDir, "cpa-manager-plus")
		info.IngressExecutable = filepath.Join(binDir, "cpamp-ingress")
		info.CPAExecutable = firstNonEmptyString(
			strings.TrimSpace(getenv("CPAMP_CPA_EXECUTABLE")),
			filepath.Join(binDir, "cli-proxy-api"),
		)
	}
	return info, nil
}

func resolvePreferredExecutable(binDir, primaryName, fallbackName string) string {
	primary := filepath.Join(binDir, primaryName)
	if _, err := os.Lstat(primary); err == nil {
		return primary
	}
	fallback := filepath.Join(binDir, fallbackName)
	if _, err := os.Lstat(fallback); err == nil {
		return fallback
	}
	return primary
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func readBaseVersionFromProvenance(provenancePath string, expectedOwnerUID int, getenv func(string) string) (string, error) {
	parentDir := filepath.Dir(provenancePath)
	parentFD, _, err := openNoFollowDirFD(parentDir)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return fallbackBaseVersion(getenv), nil
		}
		return "", fmt.Errorf("failed to open provenance dir %s: %w", parentDir, err)
	}
	defer func() { _ = unix.Close(parentFD) }()

	raw, fileSt, err := readRegularFileAtNoFollow(parentFD, filepath.Base(provenancePath), MaxSlotMetadataBytes)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return fallbackBaseVersion(getenv), nil
		}
		return "", fmt.Errorf("failed to read %s: %w", provenancePath, err)
	}
	if int(fileSt.Uid) != expectedOwnerUID {
		return "", fmt.Errorf("untrusted owner uid %d on %s", fileSt.Uid, provenancePath)
	}

	var doc baseProvenanceDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("malformed %s: %w", provenancePath, err)
	}
	norm, ok := NormalizeVersionTag(doc.Version)
	if !ok {
		return "", fmt.Errorf("invalid SemVer version %q in %s", doc.Version, provenancePath)
	}
	return norm, nil
}

func fallbackBaseVersion(getenv func(string) string) string {
	if getenv != nil {
		if envVer := strings.TrimSpace(getenv("CPAMP_BASE_VERSION")); envVer != "" {
			if norm, ok := NormalizeVersionTag(envVer); ok {
				return norm
			}
		}
	}
	if norm, ok := NormalizeVersionTag(Version); ok {
		return norm
	}
	if trimmed := strings.TrimSpace(Version); trimmed != "" {
		return trimmed
	}
	return "dev"
}

func readBaseABIFromFile(abiPath string, expectedOwnerUID int) (int, string, error) {
	defaultPlatform := ExpectedSlotPlatform()
	parentDir := filepath.Dir(abiPath)
	parentFD, _, err := openNoFollowDirFD(parentDir)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return 1, defaultPlatform, nil
		}
		return 0, "", fmt.Errorf("failed to open base-abi dir %s: %w", parentDir, err)
	}
	defer func() { _ = unix.Close(parentFD) }()

	raw, fileSt, err := readRegularFileAtNoFollow(parentFD, filepath.Base(abiPath), 64*1024)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return 1, defaultPlatform, nil
		}
		return 0, "", fmt.Errorf("failed to read %s: %w", abiPath, err)
	}
	if int(fileSt.Uid) != expectedOwnerUID {
		return 0, "", fmt.Errorf("untrusted owner uid %d on %s", fileSt.Uid, abiPath)
	}

	var doc baseABIDoc
	if err := strictUnmarshalJSON(raw, &doc); err != nil {
		return 0, "", fmt.Errorf("malformed %s: %w", abiPath, err)
	}
	if doc.SchemaVersion != 1 || doc.BaseEpoch < 1 {
		return 0, "", fmt.Errorf("invalid schema_version/base_epoch in %s", abiPath)
	}
	if doc.Platform != defaultPlatform {
		return 0, "", fmt.Errorf("base-abi platform %q does not match host %q", doc.Platform, defaultPlatform)
	}
	return doc.BaseEpoch, doc.Platform, nil
}

// ArbitratedExecutionPlan describes the verified execution target after cold-start/re-exec arbitration.
type ArbitratedExecutionPlan struct {
	Base                    BaseInstallationInfo
	Store                   *SlotStore
	State                   SlotState
	ActiveSlot              string
	SlotRoot                string
	RunningVersion          string
	TrialHold               bool
	SupervisorExecutable    string
	ManagerExecutable       string
	IngressExecutable       string
	CPAExecutable           string
	SeedConfigPath          string
	SeedLineagePath         string
	CPAArtifactManifestPath string
	RollbackLauncherExe     string
	RollbackManagerExe      string
	ExecTarget              *ExecTransition
}

// ColdStartArbitrationInput supplies inputs to ArbitrateColdStart.
type ColdStartArbitrationInput struct {
	Base            BaseInstallationInfo
	Store           *SlotStore
	DataDir         string
	ManagerUID      int
	ManagerGID      int
	SelfExePath     string
	Args            []string
	Environ         []string
	LogOut          io.Writer
	SnapshotTimeout time.Duration
}

// ArbitrateColdStart evaluates BaseVersion vs slot-state.json:
//   - BaseVersion >= SlotVersion -> adopts base, resets slot-state.json, prunes slots <= BaseVersion
//   - SlotVersion > BaseVersion -> re-verifies slot; if not yet running from slot launcher, returns ExecTarget
//   - Trial verification failure -> restores snapshot (if any), marks rolled_back, returns rollback plan/ExecTarget
func ArbitrateColdStart(in ColdStartArbitrationInput) (*ArbitratedExecutionPlan, error) {
	if in.Store == nil {
		return nil, errors.New("slot store is required")
	}
	logOut := in.LogOut
	if logOut == nil {
		logOut = os.Stderr
	}
	selfExe := in.SelfExePath
	if selfExe == "" {
		if exe, err := os.Executable(); err == nil {
			selfExe = exe
		}
	}

	makeBasePlan := func(st SlotState) *ArbitratedExecutionPlan {
		return &ArbitratedExecutionPlan{
			Base:                    in.Base,
			Store:                   in.Store,
			State:                   st,
			ActiveSlot:              "",
			SlotRoot:                "",
			RunningVersion:          in.Base.BaseVersion,
			TrialHold:               false,
			SupervisorExecutable:    in.Base.SupervisorExecutable,
			ManagerExecutable:       in.Base.ManagerExecutable,
			IngressExecutable:       in.Base.IngressExecutable,
			CPAExecutable:           in.Base.CPAExecutable,
			SeedConfigPath:          in.Base.SeedConfigPath,
			SeedLineagePath:         in.Base.SeedLineagePath,
			CPAArtifactManifestPath: in.Base.CPAArtifactManifestPath,
			RollbackLauncherExe:     in.Base.LauncherExecutable,
			RollbackManagerExe:      in.Base.ManagerExecutable,
		}
	}

	state, exists, err := in.Store.ReadSlotStateIfExists()
	if err != nil {
		return nil, err
	}
	envMap := environSliceToMap(in.Environ)

	resolveRollbackExecutables := func(st SlotState) (string, string) {
		rbLauncher := in.Base.LauncherExecutable
		rbManager := in.Base.ManagerExecutable
		if st.PreviousSlot != "" {
			prevRoot := filepath.Join(in.Store.SlotsDir, st.PreviousSlot)
			rbLauncher = filepath.Join(prevRoot, "bin", "cpamp-launcher")
			rbManager = filepath.Join(prevRoot, "bin", "cpa-manager-plus")
		}
		return rbLauncher, rbManager
	}

	planAfterColdStartRollback := func(rbState SlotState, rbExec *ExecTransition) (*ArbitratedExecutionPlan, error) {
		if rbState.ActiveSlot == "" {
			if envMap["CPAMP_ACTIVE_SLOT"] == "" && sameFileByInode(selfExe, in.Base.LauncherExecutable) {
				return makeBasePlan(rbState), nil
			}
			plan := makeBasePlan(rbState)
			plan.ExecTarget = rbExec
			return plan, nil
		}
		prevRoot := filepath.Join(in.Store.SlotsDir, rbState.ActiveSlot)
		prevLauncher := filepath.Join(prevRoot, "bin", "cpamp-launcher")
		if envMap["CPAMP_ACTIVE_SLOT"] == rbState.ActiveSlot && sameFileByInode(selfExe, prevLauncher) {
			return &ArbitratedExecutionPlan{
				Base:                    in.Base,
				Store:                   in.Store,
				State:                   rbState,
				ActiveSlot:              rbState.ActiveSlot,
				SlotRoot:                prevRoot,
				RunningVersion:          rbState.ActiveSlot,
				TrialHold:               false,
				SupervisorExecutable:    filepath.Join(prevRoot, "bin", "cpamp-runtime-supervisor"),
				ManagerExecutable:       filepath.Join(prevRoot, "bin", "cpa-manager-plus"),
				IngressExecutable:       filepath.Join(prevRoot, "bin", "cpamp-ingress"),
				CPAExecutable:           in.Base.CPAExecutable,
				SeedConfigPath:          filepath.Join(prevRoot, "share", "cpamp", "runtime", "config.seed.yaml"),
				SeedLineagePath:         filepath.Join(prevRoot, "share", "cpamp", "runtime", "seed-lineage.json"),
				CPAArtifactManifestPath: resolveSlotCPAArtifactManifestPath(in.Base, prevRoot),
				RollbackLauncherExe:     in.Base.LauncherExecutable,
				RollbackManagerExe:      in.Base.ManagerExecutable,
			}, nil
		}
		return &ArbitratedExecutionPlan{
			Base:           in.Base,
			Store:          in.Store,
			State:          rbState,
			ActiveSlot:     rbState.ActiveSlot,
			SlotRoot:       prevRoot,
			RunningVersion: rbState.ActiveSlot,
			TrialHold:      false,
			ExecTarget:     rbExec,
		}, nil
	}

	if exists && state.State == SlotStateRecoveryRequired {
		_, _ = fmt.Fprintf(
			logOut,
			"[launcher] slot-state is recovery_required for %s (%s); retrying rollback target verification and snapshot restore once\n",
			state.ActiveSlot,
			state.LastFailureCode,
		)
		rbLauncher, rbManager := resolveRollbackExecutables(state)
		recFailCode := firstNonEmptyString(state.LastFailureCode, FailureCodeSnapshotRestoreFailed)
		rbExec, rbState, rbErr := executeColdStartTrialRollback(in, state, recFailCode, rbLauncher, rbManager, logOut)
		if rbErr != nil {
			return nil, fmt.Errorf(
				"[launcher] recovery_required automatic restore failed (%w); run `%s` before starting manager",
				rbErr,
				formatRestorePreMigrateCommand(state.SnapshotDir),
			)
		}
		return planAfterColdStartRollback(rbState, rbExec)
	}

	if !exists || state.ActiveSlot == "" || state.State == SlotStateNone {
		return makeBasePlan(state), nil
	}

	// Compare BaseVersion vs ActiveSlot whenever BaseVersion is a valid SemVer 2.0 tag.
	if _, baseIsSemVer := ParseSemVerV2(in.Base.BaseVersion); baseIsSemVer {
		cmp, cmpErr := CompareSemVerV2(in.Base.BaseVersion, state.ActiveSlot)
		if cmpErr != nil {
			return nil, cmpErr
		}
		if cmp >= 0 {
			_, _ = fmt.Fprintf(
				logOut,
				"[launcher] base version %s >= slot %s; adopting base installation and pruning superseded slots\n",
				in.Base.BaseVersion,
				state.ActiveSlot,
			)
			resetState := DefaultSlotState()
			resetState.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			if err := in.Store.WriteSlotState(resetState); err != nil {
				return nil, fmt.Errorf("failed to reset superseded slot-state.json: %w", err)
			}
			_ = in.Store.PruneSlotsWhere(func(ver string) bool {
				c, err := CompareSemVerV2(ver, in.Base.BaseVersion)
				return err == nil && c <= 0
			})
			// If somehow invoked from an old slot binary, re-exec into the base launcher.
			plan := makeBasePlan(resetState)
			if !sameFileByInode(selfExe, in.Base.LauncherExecutable) && fileExistsRegular(in.Base.LauncherExecutable) {
				plan.ExecTarget = &ExecTransition{
					Path: in.Base.LauncherExecutable,
					Args: cloneArgsOrDefault(in.Args, in.Base.LauncherExecutable),
					Env:  buildLauncherExecEnv(in.Environ, in.Base.InstallRoot, in.Base.BaseVersion, ""),
				}
			}
			return plan, nil
		}
	}

	slotRoot := filepath.Join(in.Store.SlotsDir, state.ActiveSlot)
	slotLauncher := filepath.Join(slotRoot, "bin", "cpamp-launcher")
	rollbackLauncherExe, rollbackManagerExe := resolveRollbackExecutables(state)

	_, verifyErr := in.Store.VerifyInstalledSlot(state.ActiveSlot)
	if verifyErr != nil {
		if state.State == SlotStateTrial {
			failCode := mapVerifyErrToFailureCode(verifyErr)
			rbExec, rbState, rbErr := executeColdStartTrialRollback(in, state, failCode, rollbackLauncherExe, rollbackManagerExe, logOut)
			if rbErr != nil {
				return nil, rbErr
			}
			return planAfterColdStartRollback(rbState, rbExec)
		}
		return nil, fmt.Errorf("[launcher] active slot %s (%s) failed integrity/ABI verification: %w", state.ActiveSlot, state.State, verifyErr)
	}

	alreadyInTargetSlot := envMap["CPAMP_ACTIVE_SLOT"] == state.ActiveSlot && sameFileByInode(selfExe, slotLauncher)
	if state.State == SlotStateTrial && state.TrialBootCount >= 1 {
		rbExec, rbState, rbErr := executeColdStartTrialRollback(in, state, FailureCodeTrialBootLoop, rollbackLauncherExe, rollbackManagerExe, logOut)
		if rbErr != nil {
			return nil, rbErr
		}
		return planAfterColdStartRollback(rbState, rbExec)
	}

	if !alreadyInTargetSlot {
		return &ArbitratedExecutionPlan{
			Base:           in.Base,
			Store:          in.Store,
			State:          state,
			ActiveSlot:     state.ActiveSlot,
			SlotRoot:       slotRoot,
			RunningVersion: state.ActiveSlot,
			TrialHold:      state.State == SlotStateTrial,
			ExecTarget: &ExecTransition{
				Path: slotLauncher,
				Args: cloneArgsOrDefault(in.Args, slotLauncher),
				Env:  buildLauncherExecEnv(in.Environ, in.Base.InstallRoot, in.Base.BaseVersion, state.ActiveSlot),
			},
		}, nil
	}

	if state.State == SlotStateTrial {
		state.TrialBootCount = 1
		state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := in.Store.WriteSlotState(state); err != nil {
			return nil, fmt.Errorf("failed to persist trial_boot_count in slot-state.json: %w", err)
		}
	}

	return &ArbitratedExecutionPlan{
		Base:                    in.Base,
		Store:                   in.Store,
		State:                   state,
		ActiveSlot:              state.ActiveSlot,
		SlotRoot:                slotRoot,
		RunningVersion:          state.ActiveSlot,
		TrialHold:               state.State == SlotStateTrial,
		SupervisorExecutable:    filepath.Join(slotRoot, "bin", "cpamp-runtime-supervisor"),
		ManagerExecutable:       filepath.Join(slotRoot, "bin", "cpa-manager-plus"),
		IngressExecutable:       filepath.Join(slotRoot, "bin", "cpamp-ingress"),
		CPAExecutable:           in.Base.CPAExecutable,
		SeedConfigPath:          filepath.Join(slotRoot, "share", "cpamp", "runtime", "config.seed.yaml"),
		SeedLineagePath:         filepath.Join(slotRoot, "share", "cpamp", "runtime", "seed-lineage.json"),
		CPAArtifactManifestPath: resolveSlotCPAArtifactManifestPath(in.Base, slotRoot),
		RollbackLauncherExe:     rollbackLauncherExe,
		RollbackManagerExe:      rollbackManagerExe,
	}, nil
}

func resolveSlotCPAArtifactManifestPath(base BaseInstallationInfo, slotRoot string) string {
	if fileExistsRegular(base.CPAExecutable) {
		return base.CPAArtifactManifestPath
	}
	return filepath.Join(slotRoot, "share", "cpamp", "runtime", "cpa-artifact.json")
}

func mapVerifyErrToFailureCode(err error) string {
	code := slotErrorCode(err, "")
	if code != "" {
		return code
	}
	return "slot_verification_failed"
}

func executeColdStartTrialRollback(
	in ColdStartArbitrationInput,
	state SlotState,
	failureCode string,
	rollbackLauncherExe string,
	rollbackManagerExe string,
	logOut io.Writer,
) (*ExecTransition, SlotState, error) {
	cfg := TrialWatchdogConfig{
		Store:                  in.Store,
		ActiveSlot:             state.ActiveSlot,
		DataDir:                in.DataDir,
		ManagerUID:             in.ManagerUID,
		ManagerGID:             in.ManagerGID,
		RollbackLauncherExe:    rollbackLauncherExe,
		RollbackManagerExe:     rollbackManagerExe,
		BaseInstallRoot:        in.Base.InstallRoot,
		BaseVersion:            in.Base.BaseVersion,
		LauncherArgs:           in.Args,
		BaseEnviron:            in.Environ,
		SnapshotRestoreTimeout: in.SnapshotTimeout,
	}
	return ExecuteTrialRollback(cfg, state, failureCode, logOut)
}

func sameFileByInode(pathA, pathB string) bool {
	if pathA == "" || pathB == "" {
		return false
	}
	var stA, stB unix.Stat_t
	if err := unix.Stat(pathA, &stA); err != nil {
		return false
	}
	if err := unix.Stat(pathB, &stB); err != nil {
		return false
	}
	return uint64(stA.Dev) == uint64(stB.Dev) && uint64(stA.Ino) == uint64(stB.Ino)
}

func fileExistsRegular(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func cloneArgsOrDefault(args []string, fallbackArg0 string) []string {
	if len(args) == 0 {
		return []string{fallbackArg0}
	}
	out := make([]string, len(args))
	copy(out, args)
	return out
}

func environSliceToMap(environ []string) map[string]string {
	out := make(map[string]string, len(environ))
	for _, entry := range environ {
		k, v, ok := strings.Cut(entry, "=")
		if ok && k != "" {
			out[k] = v
		}
	}
	return out
}

func buildLauncherExecEnv(baseEnviron []string, baseInstallRoot, baseVersion, activeSlot string) []string {
	m := environSliceToMap(baseEnviron)
	delete(m, "CPAMP_TRIAL_HOLD")
	if baseInstallRoot != "" {
		m["CPAMP_BASE_INSTALL_ROOT"] = baseInstallRoot
	}
	if baseVersion != "" {
		m["CPAMP_BASE_VERSION"] = baseVersion
	}
	if activeSlot != "" {
		m["CPAMP_ACTIVE_SLOT"] = activeSlot
	} else {
		delete(m, "CPAMP_ACTIVE_SLOT")
	}
	return formatEnvMap(m)
}
