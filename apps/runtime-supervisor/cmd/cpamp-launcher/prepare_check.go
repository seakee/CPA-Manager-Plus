//go:build unix

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type compatSymlinkState int

const (
	compatSymlinkMissing compatSymlinkState = iota
	compatSymlinkValid
	compatSymlinkEmptyDir
)

// PIDHandle represents an open process reference (backed by a Linux pidfd in
// production) that stays bound to a single process incarnation even if its
// numeric PID is later reused.
type PIDHandle interface {
	Signal(sig syscall.Signal) error
	Close() error
}

// checkPreparationState validates existing paths before any volume mutation.
// Missing paths are left for preparation to create; existing untrusted paths
// are never repaired. Check parents before children to avoid following links.
func checkPreparationState(layout RuntimeLayout, explicitToken string, sys System) error {
	if _, err := checkDataDirectory(layout.DataDir, sys); err != nil {
		return err
	}
	for _, dir := range layout.TrustedRootOwnedDirs() {
		if _, err := checkRootOwnedDirectory(dir, sys); err != nil {
			return err
		}
	}
	for _, dir := range layout.GatewayDirs() {
		if _, err := checkRealGatewayDirectory(dir, sys); err != nil {
			return err
		}
	}
	if _, err := checkRuntimeToken(layout.TokenFile, explicitToken, sys); err != nil {
		return err
	}
	if _, err := checkGatewayConfigFile(layout.GatewayConfigPath, sys); err != nil {
		return err
	}
	if layout.RuntimeRoot != DedicatedRuntimeMountPoint {
		if _, err := checkRuntimeCompatSymlinkState(layout.RuntimeRoot, sys); err != nil {
			return err
		}
	}
	return nil
}

func checkDataDirectory(dataDir string, sys System) (bool, error) {
	stat, err := sys.Lstat(dataDir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to stat data directory %s: %w", dataDir, err)
	}
	if stat.Mode&fs.ModeSymlink != 0 || !stat.Mode.IsDir() {
		return false, fmt.Errorf("Data directory is not a trusted directory: %s", dataDir)
	}
	return true, nil
}

func requireRootOwnedDirectory(dir string, sys System) error {
	stat, err := sys.Lstat(dir)
	if err != nil || stat.Mode&fs.ModeSymlink != 0 || !stat.Mode.IsDir() {
		return fmt.Errorf("Supervisor persisted directory is not a trusted directory: %s", dir)
	}
	if stat.UID != RootUID {
		return fmt.Errorf("Supervisor persisted directory owner is not root: %s (uid %d)", dir, stat.UID)
	}
	return nil
}

func checkRootOwnedDirectory(dir string, sys System) (bool, error) {
	if _, err := sys.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err := requireRootOwnedDirectory(dir, sys); err != nil {
		return false, err
	}
	return true, nil
}

func requireRealGatewayDirectory(dir string, sys System) error {
	stat, err := sys.Lstat(dir)
	if err != nil || stat.Mode&fs.ModeSymlink != 0 || !stat.Mode.IsDir() {
		return fmt.Errorf("Gateway state path is not a real directory: %s", dir)
	}
	return nil
}

func checkRealGatewayDirectory(dir string, sys System) (bool, error) {
	if _, err := sys.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err := requireRealGatewayDirectory(dir, sys); err != nil {
		return false, err
	}
	return true, nil
}

func checkGatewayConfigFile(configPath string, sys System) (bool, error) {
	stat, err := sys.Lstat(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil || stat.Mode&fs.ModeSymlink != 0 || !stat.Mode.IsRegular() {
		return false, errors.New("failed to initialize Gateway configuration")
	}
	return true, nil
}

func checkRuntimeCompatSymlinkState(runtimeRoot string, sys System) (compatSymlinkState, error) {
	stat, err := sys.Lstat(DedicatedRuntimeMountPoint)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return compatSymlinkMissing, nil
	case err != nil:
		return compatSymlinkMissing, fmt.Errorf("failed to stat %s: %w", DedicatedRuntimeMountPoint, err)
	case stat.Mode&fs.ModeSymlink != 0:
		target, linkErr := sys.Readlink(DedicatedRuntimeMountPoint)
		if linkErr != nil || target != runtimeRoot {
			return compatSymlinkMissing, fmt.Errorf("unexpected symlink at %s -> %s (want %s)", DedicatedRuntimeMountPoint, target, runtimeRoot)
		}
		return compatSymlinkValid, nil
	case stat.Mode.IsDir():
		entries, dirErr := sys.ReadDir(DedicatedRuntimeMountPoint)
		if dirErr != nil || len(entries) > 0 {
			return compatSymlinkMissing, fmt.Errorf("unmounted %s directory is not empty", DedicatedRuntimeMountPoint)
		}
		return compatSymlinkEmptyDir, nil
	default:
		return compatSymlinkMissing, fmt.Errorf("unexpected file at %s", DedicatedRuntimeMountPoint)
	}
}

// checkRuntimeToken returns the existing validated token, or empty when absent.
func checkRuntimeToken(tokenFile, explicitToken string, sys System) (string, error) {
	if explicitToken != "" {
		if err := validateRuntimeToken(explicitToken); err != nil {
			return "", err
		}
	}
	stat, err := sys.Lstat(tokenFile)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to stat Runtime transport secret %s: %w", tokenFile, err)
	}
	if stat.Mode&fs.ModeSymlink != 0 || !stat.Mode.IsRegular() {
		return "", errors.New("failed to initialize Runtime transport secret")
	}
	tokenBytes, err := sys.ReadFile(tokenFile)
	if err != nil {
		return "", errors.New("failed to initialize Runtime transport secret")
	}
	token := string(tokenBytes)
	if err := validateRuntimeToken(token); err != nil {
		return "", err
	}
	if explicitToken != "" && token != explicitToken {
		return "", errors.New("explicit Runtime transport secret does not match existing secret source")
	}
	return token, nil
}

// ensureCPAQuiescence enumerates procPath, opens a pidfd for any process whose
// real, effective, saved, or filesystem UID in /proc/<pid>/status equals
// cpaUID, re-verifies the process's /proc/<pid>/status while holding the pidfd
// (so a reused PID is never signalled if it now belongs to Manager/Ingress or
// another non-CPA identity), sends SIGKILL through the pidfd, and repeats until
// two consecutive scans find no matching processes.
// PID 1, the launcher itself, Manager (10002), and Ingress (10003) are never signaled.
func ensureCPAQuiescence(
	ctx context.Context,
	procPath string,
	cpaUID int,
	timeout time.Duration,
	reapChildren func(),
	sys System,
) error {
	if procPath == "" {
		procPath = DefaultProcPath
	}
	if timeout <= 0 {
		timeout = CPAQuiescenceTimeout
	}
	selfPID := os.Getpid()
	deadline := time.Now().Add(timeout)
	consecutiveClean := 0

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if reapChildren != nil {
			reapChildren()
		}

		matched, err := killCPAProcessesInProc(procPath, cpaUID, selfPID, sys)
		if err != nil {
			return err
		}
		if len(matched) == 0 {
			consecutiveClean++
			if consecutiveClean >= 2 {
				return nil
			}
			continue
		}

		consecutiveClean = 0
		if reapChildren != nil {
			reapChildren()
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("failed to quiesce CPA UID %d processes within %s (remaining pids: %v)", cpaUID, timeout, matched)
		}

		timer := time.NewTimer(CPAQuiescencePollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func killCPAProcessesInProc(procPath string, cpaUID, selfPID int, sys System) ([]int, error) {
	entries, err := sys.ReadDir(procPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read proc directory %s: %w", procPath, err)
	}

	var matched []int
	for _, entry := range entries {
		name := entry.Name()
		if !isDecimalDigits(name) {
			continue
		}
		pid, convErr := strconv.Atoi(name)
		if convErr != nil || pid <= 1 || pid == selfPID {
			continue
		}
		statusPath := filepath.Join(procPath, name, "status")
		matchedInitial, err := statusFileMatchesCPA(statusPath, cpaUID, sys)
		if err != nil {
			return nil, err
		}
		if !matchedInitial {
			continue
		}

		handle, openErr := sys.OpenPID(pid)
		if errors.Is(openErr, syscall.ESRCH) || errors.Is(openErr, fs.ErrNotExist) {
			continue
		}
		if openErr != nil {
			if errors.Is(openErr, syscall.ENOSYS) {
				return nil, fmt.Errorf("pidfd_open is required to quiesce CPA processes safely: %w", openErr)
			}
			return nil, fmt.Errorf("failed to open process handle for pid %d: %w", pid, openErr)
		}

		// With the process handle (pidfd) held, re-read /proc/<pid>/status to
		// confirm the numeric PID still belongs to the CPA UID and was not
		// reused by Manager, Ingress, or another identity before OpenPID.
		stillMatches, verifyErr := statusFileMatchesCPA(statusPath, cpaUID, sys)
		if verifyErr != nil {
			_ = handle.Close()
			return nil, verifyErr
		}
		if !stillMatches {
			_ = handle.Close()
			continue
		}

		matched = append(matched, pid)
		sigErr := signalPIDHandle(handle, pid, syscall.SIGKILL, sys)
		_ = handle.Close()
		if sigErr != nil && !errors.Is(sigErr, syscall.ESRCH) && !errors.Is(sigErr, fs.ErrNotExist) {
			return nil, fmt.Errorf("failed to kill residual CPA process %d: %w", pid, sigErr)
		}
	}
	return matched, nil
}

func signalPIDHandle(handle PIDHandle, pid int, sig syscall.Signal, sys System) error {
	if _, isDelegate := handle.(sysKillPIDHandle); isDelegate {
		return sys.Kill(pid, sig)
	}
	return handle.Signal(sig)
}

type sysKillPIDHandle struct{}

func (sysKillPIDHandle) Signal(syscall.Signal) error { return nil }
func (sysKillPIDHandle) Close() error                { return nil }

func statusFileMatchesCPA(statusPath string, cpaUID int, sys System) (bool, error) {
	statusBytes, readErr := sys.ReadFile(statusPath)
	if errors.Is(readErr, fs.ErrNotExist) || errors.Is(readErr, syscall.ESRCH) {
		return false, nil
	}
	if readErr != nil {
		return false, fmt.Errorf("failed to read %s: %w", statusPath, readErr)
	}
	uids, ok := parseProcStatusUIDs(statusBytes)
	if !ok {
		return false, fmt.Errorf("invalid Uid line in %s", statusPath)
	}
	return procUIDsMatchCPA(uids, cpaUID), nil
}

func parseProcStatusUIDs(statusBytes []byte) ([4]int, bool) {
	var uids [4]int
	for _, rawLine := range strings.Split(string(statusBytes), "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "Uid:"))
		if len(fields) < 4 {
			return uids, false
		}
		for i := range 4 {
			if !isDecimalDigits(fields[i]) {
				return uids, false
			}
			val, err := strconv.Atoi(fields[i])
			if err != nil || val < 0 {
				return uids, false
			}
			uids[i] = val
		}
		return uids, true
	}
	return uids, false
}

func procUIDsMatchCPA(uids [4]int, cpaUID int) bool {
	hasCPA := false
	for _, uid := range uids {
		if uid == ManagerUID || uid == IngressUID {
			return false
		}
		if uid == cpaUID {
			hasCPA = true
		}
	}
	return hasCPA
}
