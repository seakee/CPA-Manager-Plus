//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	DefaultSnapshotRestoreTimeout = 2 * time.Minute
	DefaultTrialReleaseTimeout    = 5 * time.Second
	TrialReleaseRetryInterval     = 100 * time.Millisecond

	FailureCodeSupervisorStartupFailed = "supervisor_startup_failed"
	FailureCodeManagerStartupFailed    = "manager_startup_failed"
	FailureCodeIngressStartupFailed    = "ingress_startup_failed"
	FailureCodeTrialTimeout            = "trial_timeout"
	FailureCodeTrialBootLoop           = "trial_boot_loop"
	FailureCodeSnapshotRestoreFailed   = "snapshot_restore_failed"
	FailureCodeRollbackTargetInvalid   = "rollback_target_invalid"
)

// ExecTransition describes an in-place unix.Exec replacement of cpamp-launcher.
type ExecTransition struct {
	Path string
	Args []string
	Env  []string
}

// ExecuteProcessReplacement replaces the current process image via unix.Exec.
func ExecuteProcessReplacement(tr *ExecTransition) error {
	if tr == nil || strings.TrimSpace(tr.Path) == "" {
		return fmt.Errorf("invalid exec transition target")
	}
	args := tr.Args
	if len(args) == 0 {
		args = []string{tr.Path}
	}
	return unix.Exec(tr.Path, args, tr.Env)
}

// TrialWatchdogConfig configures the 60s Trial readiness watchdog and rollback path.
type TrialWatchdogConfig struct {
	Store                  *SlotStore
	ActiveSlot             string
	LauncherToken          string
	ManagerAddr            string
	DataDir                string
	ManagerUID             int
	ManagerGID             int
	RollbackLauncherExe    string
	RollbackManagerExe     string
	BaseInstallRoot        string
	BaseVersion            string
	LauncherArgs           []string
	BaseEnviron            []string
	SnapshotRestoreTimeout time.Duration
	TrialReleaseTimeout    time.Duration
}

// persistCommittedTrialState durably writes state="committed" to slot-state.json
// after verifying an optional preWriteCheck immediately prior to WriteSlotState.
func persistCommittedTrialState(cfg TrialWatchdogConfig, preWriteCheck func() error) (SlotState, error) {
	if cfg.Store == nil {
		return SlotState{}, errors.New("slot store is required for trial commit")
	}
	state, exists, err := cfg.Store.ReadSlotStateIfExists()
	if err != nil {
		return SlotState{}, fmt.Errorf("read slot-state before trial commit: %w", err)
	}
	if !exists {
		state = DefaultSlotState()
		state.ActiveSlot = cfg.ActiveSlot
	}
	lastAttempt := firstNonEmptyString(state.LastAttemptID, state.ActiveAttemptID, state.TrialID)
	state.State = SlotStateCommitted
	state.TrialID = ""
	state.TrialBootCount = 0
	state.ActiveAttemptID = ""
	state.LastAttemptID = lastAttempt
	state.SnapshotDir = ""
	state.LastFailureCode = ""
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if preWriteCheck != nil {
		if err := preWriteCheck(); err != nil {
			return SlotState{}, err
		}
	}
	if err := cfg.Store.WriteSlotState(state); err != nil {
		return SlotState{}, fmt.Errorf("write committed slot-state: %w", err)
	}
	return state, nil
}

func finalizeCommittedTrialRelease(ctx context.Context, cfg TrialWatchdogConfig, state SlotState, logOut io.Writer) bool {
	if logOut == nil {
		logOut = os.Stderr
	}
	if cfg.Store != nil {
		_ = cfg.Store.PruneSlotsWhere(func(ver string) bool {
			return ver != state.ActiveSlot && ver != state.PreviousSlot
		})
	}
	released := releaseManagerTrialHold(ctx, cfg.ManagerAddr, cfg.LauncherToken, cfg.TrialReleaseTimeout)
	if !released {
		_, _ = fmt.Fprintf(logOut, "[launcher] warning: POST /internal/v1/trial/release did not acknowledge within window; restarting manager cleanly without CPAMP_TRIAL_HOLD\n")
	} else {
		_, _ = fmt.Fprintf(logOut, "[launcher] trial %s committed and write-hold released\n", state.ActiveSlot)
	}
	return released
}

// CommitActiveTrial marks slot-state.json as committed, calls Manager POST /internal/v1/trial/release,
// and prunes staged slots other than active_slot and previous_slot.
func CommitActiveTrial(ctx context.Context, cfg TrialWatchdogConfig, logOut io.Writer) (bool, error) {
	state, err := persistCommittedTrialState(cfg, nil)
	if err != nil {
		return false, err
	}
	return finalizeCommittedTrialRelease(ctx, cfg, state, logOut), nil
}

func releaseManagerTrialHold(parentCtx context.Context, managerAddr, launcherToken string, timeout time.Duration) bool {
	if strings.TrimSpace(managerAddr) == "" || launcherToken == "" {
		return false
	}
	if timeout <= 0 {
		timeout = DefaultTrialReleaseTimeout
	}
	releaseURL := "http://" + strings.TrimSpace(managerAddr) + "/internal/v1/trial/release"
	client := &http.Client{
		Timeout: 1 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	ctx, cancel := context.WithTimeout(parentCtx, timeout)
	defer cancel()

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, releaseURL, bytes.NewReader([]byte("{}")))
		if err != nil {
			return false
		}
		req.Header.Set("Authorization", "Bearer "+launcherToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return true
			}
		}

		timer := time.NewTimer(TrialReleaseRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

// ExecuteTrialRollback re-verifies the rollback target (previous slot or base binaries),
// restores the required pre-slot database snapshot via manager-data-snapshot restore,
// updates slot-state.json to rolled_back, and returns the ExecTransition back to the rollback target.
// If rollback target verification fails, snapshot_dir is missing, or snapshot restore fails,
// it writes state="recovery_required" (preserving snapshot_dir and last_attempt_id) and fails closed.
func ExecuteTrialRollback(
	cfg TrialWatchdogConfig,
	fallbackState SlotState,
	failureCode string,
	logOut io.Writer,
) (*ExecTransition, SlotState, error) {
	if logOut == nil {
		logOut = os.Stderr
	}
	if cfg.Store == nil {
		return nil, SlotState{}, errors.New("slot store is required for trial rollback")
	}
	state, exists, err := cfg.Store.ReadSlotStateIfExists()
	if err != nil || !exists {
		state = fallbackState
	} else {
		state.SnapshotDir = firstNonEmptyString(state.SnapshotDir, fallbackState.SnapshotDir)
		if state.PreviousSlot == "" {
			state.PreviousSlot = fallbackState.PreviousSlot
		}
		if state.PreviousSchemaGeneration <= 0 {
			state.PreviousSchemaGeneration = fallbackState.PreviousSchemaGeneration
		}
		if state.SchemaGeneration <= 0 {
			state.SchemaGeneration = fallbackState.SchemaGeneration
		}
		if state.BaseVersionAtSwitch == "" {
			state.BaseVersionAtSwitch = fallbackState.BaseVersionAtSwitch
		}
	}
	failedSlot := firstNonEmptyString(state.ActiveSlot, fallbackState.ActiveSlot, cfg.ActiveSlot)
	lastAttempt := firstNonEmptyString(
		state.LastAttemptID,
		state.ActiveAttemptID,
		state.TrialID,
		fallbackState.LastAttemptID,
		fallbackState.ActiveAttemptID,
		fallbackState.TrialID,
	)
	if failureCode == "" {
		failureCode = FailureCodeTrialTimeout
	}

	_, _ = fmt.Fprintf(
		logOut,
		"[launcher] trial %s failed (%s); rolling back before opening writes\n",
		failedSlot,
		failureCode,
	)

	enterRecoveryRequired := func(recFailCode string, cause error) (*ExecTransition, SlotState, error) {
		if recFailCode == "" {
			recFailCode = FailureCodeSnapshotRestoreFailed
		}
		curSchema := state.SchemaGeneration
		if curSchema <= 0 {
			curSchema = 1
		}
		prevSchema := state.PreviousSchemaGeneration
		if prevSchema <= 0 {
			prevSchema = 1
		}
		recState := SlotState{
			SchemaVersion:            1,
			State:                    SlotStateRecoveryRequired,
			ActiveSlot:               failedSlot,
			PreviousSlot:             state.PreviousSlot,
			BaseVersionAtSwitch:      state.BaseVersionAtSwitch,
			TrialID:                  state.TrialID,
			TrialBootCount:           state.TrialBootCount,
			ActiveAttemptID:          "",
			LastAttemptID:            lastAttempt,
			SnapshotDir:              state.SnapshotDir,
			SchemaGeneration:         curSchema,
			PreviousSchemaGeneration: prevSchema,
			LastFailureCode:          recFailCode,
			LastRolledBackSlot:       state.LastRolledBackSlot,
			UpdatedAt:                time.Now().UTC().Format(time.RFC3339),
		}
		restoreCmd := formatRestorePreMigrateCommand(state.SnapshotDir)
		if writeErr := cfg.Store.WriteSlotState(recState); writeErr != nil {
			return nil, SlotState{}, fmt.Errorf(
				"trial rollback for %s failed (%s: %v) and failed writing recovery_required slot-state: %w; run `%s`",
				failedSlot,
				recFailCode,
				cause,
				writeErr,
				restoreCmd,
			)
		}
		_, _ = fmt.Fprintf(
			logOut,
			"[launcher] ERROR: trial rollback for %s failed (%s: %v); persisted state=recovery_required. Restore manually with `%s` before starting manager.\n",
			failedSlot,
			recFailCode,
			cause,
			restoreCmd,
		)
		return nil, recState, fmt.Errorf(
			"trial rollback for %s entered recovery_required (%s): %w; run `%s`",
			failedSlot,
			recFailCode,
			cause,
			restoreCmd,
		)
	}

	targetLauncherExe, targetManagerExe, verFailCode, verErr := verifyRollbackTargetExecutables(cfg, state.PreviousSlot)
	if verErr != nil {
		return enterRecoveryRequired(verFailCode, verErr)
	}

	snapDir := strings.TrimSpace(state.SnapshotDir)
	if snapDir == "" {
		return enterRecoveryRequired(FailureCodeSnapshotRestoreFailed, errors.New("trial state is missing required snapshot_dir"))
	}

	restoreCfg := cfg
	restoreCfg.RollbackLauncherExe = targetLauncherExe
	restoreCfg.RollbackManagerExe = targetManagerExe
	if restoreErr := runManagerSnapshotRestore(restoreCfg, snapDir, logOut); restoreErr != nil {
		return enterRecoveryRequired(FailureCodeSnapshotRestoreFailed, restoreErr)
	}

	prevGen := state.PreviousSchemaGeneration
	if prevGen <= 0 {
		prevGen = 1
	}
	rolledBackState := SlotState{
		SchemaVersion:            1,
		State:                    SlotStateRolledBack,
		ActiveSlot:               state.PreviousSlot,
		PreviousSlot:             "",
		BaseVersionAtSwitch:      state.BaseVersionAtSwitch,
		TrialID:                  "",
		TrialBootCount:           0,
		ActiveAttemptID:          "",
		LastAttemptID:            lastAttempt,
		SnapshotDir:              "",
		SchemaGeneration:         prevGen,
		PreviousSchemaGeneration: 1,
		LastFailureCode:          failureCode,
		LastRolledBackSlot:       failedSlot,
		UpdatedAt:                time.Now().UTC().Format(time.RFC3339),
	}
	if err := cfg.Store.WriteSlotState(rolledBackState); err != nil {
		return nil, SlotState{}, fmt.Errorf("failed to write rolled_back slot-state.json: %w", err)
	}

	tr := &ExecTransition{
		Path: targetLauncherExe,
		Args: cloneArgsOrDefault(cfg.LauncherArgs, targetLauncherExe),
		Env:  buildLauncherExecEnv(cfg.BaseEnviron, cfg.BaseInstallRoot, cfg.BaseVersion, state.PreviousSlot),
	}
	return tr, rolledBackState, nil
}

func verifyRollbackTargetExecutables(cfg TrialWatchdogConfig, previousSlot string) (string, string, string, error) {
	if previousSlot != "" {
		if _, err := cfg.Store.VerifyInstalledSlot(previousSlot); err != nil {
			code := slotErrorCode(err, FailureCodeRollbackTargetInvalid)
			return "", "", code, fmt.Errorf("previous slot %s failed verification: %w", previousSlot, err)
		}
		prevRoot := filepath.Join(cfg.Store.SlotsDir, previousSlot)
		launcherExe := strings.TrimSpace(cfg.RollbackLauncherExe)
		if launcherExe == "" {
			launcherExe = filepath.Join(prevRoot, "bin", "cpamp-launcher")
		}
		managerExe := strings.TrimSpace(cfg.RollbackManagerExe)
		if managerExe == "" {
			managerExe = filepath.Join(prevRoot, "bin", "cpa-manager-plus")
		}
		return launcherExe, managerExe, "", nil
	}

	launcherExe := strings.TrimSpace(cfg.RollbackLauncherExe)
	managerExe := strings.TrimSpace(cfg.RollbackManagerExe)
	if err := verifyTrustedRegularExecutable(cfg.Store, launcherExe, "base launcher executable"); err != nil {
		return "", "", FailureCodeRollbackTargetInvalid, err
	}
	if err := verifyTrustedRegularExecutable(cfg.Store, managerExe, "base manager executable"); err != nil {
		return "", "", FailureCodeRollbackTargetInvalid, err
	}
	return launcherExe, managerExe, "", nil
}

func verifyTrustedRegularExecutable(store *SlotStore, exePath, desc string) error {
	clean := filepath.Clean(strings.TrimSpace(exePath))
	if exePath == "" || !filepath.IsAbs(clean) {
		return fmt.Errorf("%s path %q is empty or relative", desc, exePath)
	}
	fd, err := unix.Open(clean, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("failed opening %s %s: %w", desc, clean, err)
	}
	defer func() { _ = unix.Close(fd) }()

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("failed stating %s %s: %w", desc, clean, err)
	}
	if (uint32(st.Mode) & unix.S_IFMT) != unix.S_IFREG {
		return fmt.Errorf("%s %s is not a regular file", desc, clean)
	}
	if store != nil {
		if err := store.checkStatOwner(st, clean); err != nil {
			return fmt.Errorf("%s failed owner check: %w", desc, err)
		}
	}
	if uint32(st.Mode)&0o022 != 0 {
		return fmt.Errorf("%s %s is group/world writable (%#o)", desc, clean, uint32(st.Mode)&0o7777)
	}
	if uint32(st.Mode)&0o111 == 0 || st.Size <= 0 {
		return fmt.Errorf("%s %s is not a non-empty executable file", desc, clean)
	}
	return nil
}

func runManagerSnapshotRestore(cfg TrialWatchdogConfig, snapshotDir string, logOut io.Writer) error {
	if strings.TrimSpace(cfg.RollbackManagerExe) == "" {
		return fmt.Errorf("rollback manager executable path is empty")
	}
	dataDir := strings.TrimSpace(cfg.DataDir)
	if dataDir == "" {
		dataDir = DefaultDataDir
	}
	expectedUID := cfg.ManagerUID
	if expectedUID == 0 && cfg.Store != nil {
		expectedUID = cfg.Store.ExpectedUID
	}

	cleanSnapDir := filepath.Clean(strings.TrimSpace(snapshotDir))
	manifest, err := readValidatedManagerSnapshotManifest(cleanSnapDir, dataDir, expectedUID)
	if err != nil {
		return fmt.Errorf("validate snapshot before restore: %w", err)
	}

	timeout := cfg.SnapshotRestoreTimeout
	if timeout <= 0 {
		timeout = DefaultSnapshotRestoreTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	envMap := environSliceToMap(cfg.BaseEnviron)
	stripLauncherOnlyEnvVars(envMap)
	delete(envMap, "CPAMP_RUNTIME_TOKEN")
	delete(envMap, "CPAMP_RUNTIME_TOKEN_FILE")
	if strings.TrimSpace(envMap["PATH"]) == "" {
		envMap["PATH"] = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}

	cmd := exec.CommandContext(
		ctx,
		cfg.RollbackManagerExe,
		"manager-data-snapshot",
		"restore",
		"--db-path", manifest.TargetDBPath,
		"--data-key-path", manifest.TargetDataKeyPath,
		"--snapshot-dir", cleanSnapDir,
	)
	cmd.Dir = dataDir
	cmd.Env = formatEnvMap(envMap)
	sysAttr := &syscall.SysProcAttr{Setpgid: true}
	if os.Geteuid() == 0 && cfg.ManagerUID > 0 {
		sysAttr.Credential = &syscall.Credential{
			Uid:    uint32(cfg.ManagerUID),
			Gid:    uint32(cfg.ManagerGID),
			Groups: []uint32{uint32(cfg.ManagerGID)},
		}
	}
	cmd.SysProcAttr = sysAttr

	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("manager-data-snapshot restore failed (%w): %s", err, strings.TrimSpace(outBuf.String()))
	}
	if trimmed := strings.TrimSpace(outBuf.String()); trimmed != "" {
		_, _ = fmt.Fprintf(logOut, "[launcher] snapshot restored: %s\n", trimmed)
	}
	return nil
}

// WriteSwitchSlotState writes slot-state.json immediately prior to unix.Exec for an online activate or rollback.
func WriteSwitchSlotState(store *SlotStore, baseVersion, currentActiveSlot string, action SlotSwitchAction) error {
	now := time.Now().UTC().Format(time.RFC3339)
	prevState, _, _ := store.ReadSlotStateIfExists()
	prevLastAttempt := firstNonEmptyString(prevState.LastAttemptID, prevState.ActiveAttemptID, prevState.TrialID)

	switch action.Kind {
	case SlotSwitchActivate:
		attemptID := strings.TrimSpace(action.AttemptID)
		if attemptID == "" {
			suffix, err := randomHexToken(8)
			if err != nil {
				return err
			}
			attemptID = "attempt-" + suffix
		}
		return store.WriteSlotState(SlotState{
			SchemaVersion:            1,
			State:                    SlotStateTrial,
			ActiveSlot:               action.TargetVersion,
			PreviousSlot:             currentActiveSlot,
			BaseVersionAtSwitch:      baseVersion,
			TrialID:                  attemptID,
			TrialBootCount:           0,
			ActiveAttemptID:          attemptID,
			LastAttemptID:            attemptID,
			SnapshotDir:              action.SnapshotDir,
			SchemaGeneration:         action.TargetSchemaGeneration,
			PreviousSchemaGeneration: action.PreviousSchemaGeneration,
			LastFailureCode:          "",
			LastRolledBackSlot:       "",
			UpdatedAt:                now,
		})

	case SlotSwitchRollback:
		if action.RollbackTargetSlot != "" {
			return store.WriteSlotState(SlotState{
				SchemaVersion:            1,
				State:                    SlotStateCommitted,
				ActiveSlot:               action.RollbackTargetSlot,
				PreviousSlot:             "",
				BaseVersionAtSwitch:      baseVersion,
				TrialID:                  "",
				TrialBootCount:           0,
				ActiveAttemptID:          "",
				LastAttemptID:            prevLastAttempt,
				SnapshotDir:              "",
				SchemaGeneration:         action.RollbackSchemaGeneration,
				PreviousSchemaGeneration: 1,
				LastFailureCode:          "",
				LastRolledBackSlot:       action.ExpectedActiveSlot,
				UpdatedAt:                now,
			})
		}
		return store.WriteSlotState(SlotState{
			SchemaVersion:            1,
			State:                    SlotStateNone,
			ActiveSlot:               "",
			PreviousSlot:             "",
			BaseVersionAtSwitch:      baseVersion,
			TrialID:                  "",
			TrialBootCount:           0,
			ActiveAttemptID:          "",
			LastAttemptID:            prevLastAttempt,
			SnapshotDir:              "",
			SchemaGeneration:         action.RollbackSchemaGeneration,
			PreviousSchemaGeneration: action.RollbackSchemaGeneration,
			LastFailureCode:          "",
			LastRolledBackSlot:       action.ExpectedActiveSlot,
			UpdatedAt:                now,
		})

	default:
		return fmt.Errorf("unknown slot switch action %d", action.Kind)
	}
}

// NormalizeTCPProbeAddr maps wildcard bind hosts (0.0.0.0, ::, "") to loopback
// so tcpProbeReady can dial Ingress public listeners bound to 0.0.0.0:18317.
func NormalizeTCPProbeAddr(bindAddr string) string {
	trimmed := strings.TrimSpace(bindAddr)
	if trimmed == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(trimmed)
	if err != nil {
		return trimmed
	}
	switch host {
	case "", "0.0.0.0":
		return net.JoinHostPort("127.0.0.1", port)
	case "::":
		return net.JoinHostPort("::1", port)
	default:
		return trimmed
	}
}

func formatRestorePreMigrateCommand(snapshotDir string) string {
	trimmed := strings.TrimSpace(snapshotDir)
	if trimmed != "" {
		return fmt.Sprintf("cpa-manager-plus restore-pre-migrate --snapshot-dir %s --confirm", shellQuotePOSIXArg(trimmed))
	}
	return "cpa-manager-plus restore-pre-migrate --latest --confirm"
}

func shellQuotePOSIXArg(arg string) string {
	if arg == "" {
		return "''"
	}
	safe := true
	for i := range len(arg) {
		c := arg[i]
		if (c >= 'A' && c <= 'Z') ||
			(c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') ||
			c == '_' || c == '.' || c == '/' || c == ':' || c == '@' || c == '=' || c == '-' {
			continue
		}
		safe = false
		break
	}
	if safe {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}
