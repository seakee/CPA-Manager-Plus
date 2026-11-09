//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// ProcSuperMagic is the Linux statfs f_type magic for procfs (PROC_SUPER_MAGIC).
	ProcSuperMagic int64 = 0x9fa0
)

type compatSymlinkState int

const (
	compatSymlinkMissing compatSymlinkState = iota
	compatSymlinkValid
	compatSymlinkEmptyDir
)

type uidClassification int

const (
	uidClassNoCPA uidClassification = iota
	uidClassPureCPA
	uidClassMixed
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

// validatePrivatePIDNamespaceAndProcfs enforces that cpamp-launcher is PID 1 of
// a private PID namespace and that procPath is a genuine, unfiltered procfs
// mount (no hidepid) for that namespace before any /proc enumeration or
// signalling can occur.
func validatePrivatePIDNamespaceAndProcfs(procPath string, sys System) (int, error) {
	selfPID := sys.Getpid()
	if selfPID != 1 {
		return 0, fmt.Errorf(
			"cpamp-launcher must run as PID 1 in a private PID namespace (got pid %d); do not use --pid=host, --pid=container, or --init",
			selfPID,
		)
	}
	cleanProcPath := filepath.Clean(procPath)
	stat, err := sys.Lstat(cleanProcPath)
	if err != nil {
		return 0, fmt.Errorf("failed to stat procfs at %s: %w", cleanProcPath, err)
	}
	if stat.Mode&fs.ModeSymlink != 0 || !stat.Mode.IsDir() {
		return 0, fmt.Errorf("proc path %s is not a real directory", cleanProcPath)
	}
	fsMagic, err := sys.StatfsType(cleanProcPath)
	if err != nil {
		return 0, fmt.Errorf("failed to statfs proc path %s: %w", cleanProcPath, err)
	}
	if fsMagic != ProcSuperMagic {
		return 0, fmt.Errorf("proc path %s is not a procfs mount (magic 0x%x, want 0x%x)", cleanProcPath, fsMagic, ProcSuperMagic)
	}
	procSelfPath := filepath.Join(cleanProcPath, "self")
	selfTarget, err := sys.Readlink(procSelfPath)
	if err != nil {
		return 0, fmt.Errorf("failed to readlink %s: %w", procSelfPath, err)
	}
	if strings.TrimSpace(selfTarget) != "1" {
		return 0, fmt.Errorf("proc self at %s resolved to %q, want \"1\" for private PID namespace", procSelfPath, selfTarget)
	}
	mntID, err := sys.MountID(cleanProcPath)
	if err != nil {
		return 0, fmt.Errorf("failed to resolve mount ID for %s (cpamp-launcher requires an unfiltered procfs with no hidepid): %w", cleanProcPath, err)
	}

	mountInfoPath := filepath.Join(cleanProcPath, "self", "mountinfo")
	mountInfoBytes, err := sys.ReadFile(mountInfoPath)
	if err != nil {
		return 0, fmt.Errorf("failed to read %s (cpamp-launcher requires an unfiltered procfs with no hidepid): %w", mountInfoPath, err)
	}
	if err := validateUnfilteredProcMountInfo(cleanProcPath, mountInfoPath, mntID, mountInfoBytes); err != nil {
		return 0, err
	}
	return selfPID, nil
}

// validateUnfilteredProcMountInfo locates the mountinfo record whose mount ID
// (field 1) equals wantMountID (obtained from <procPath>/self/fdinfo/<fd>),
// requires its decoded mount point to equal cleanProcPath and its fstype to
// equal "proc", and refuses any hidepid option other than "0" or "off".
func validateUnfilteredProcMountInfo(cleanProcPath, mountInfoPath string, wantMountID int, mountInfoBytes []byte) error {
	var (
		found         bool
		matchedFields []string
		matchedSepIdx int
	)
	for _, rawLine := range bytes.Split(mountInfoBytes, []byte{'\n'}) {
		line := strings.TrimSpace(string(rawLine))
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 10 || !isDecimalDigits(fields[0]) {
			continue
		}
		recMountID, err := strconv.Atoi(fields[0])
		if err != nil || recMountID != wantMountID {
			continue
		}
		sepIdx := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				sepIdx = i
				break
			}
		}
		if sepIdx < 0 || len(fields) < sepIdx+4 {
			return fmt.Errorf("malformed mountinfo entry (mnt_id %d) for %s in %s; cpamp-launcher requires an unfiltered procfs (no hidepid)", wantMountID, cleanProcPath, mountInfoPath)
		}
		found = true
		matchedFields = fields
		matchedSepIdx = sepIdx
		break
	}
	if !found {
		return fmt.Errorf("procfs mount entry with mnt_id %d for %s not found in %s; cpamp-launcher requires an unfiltered procfs (no hidepid)", wantMountID, cleanProcPath, mountInfoPath)
	}

	gotMountPoint := unescapeMountInfoPath(matchedFields[4])
	if gotMountPoint != cleanProcPath {
		return fmt.Errorf("mount entry with mnt_id %d in %s has mount point %q, want %q; cpamp-launcher requires an unfiltered procfs (no hidepid)", wantMountID, mountInfoPath, gotMountPoint, cleanProcPath)
	}

	fstype := matchedFields[matchedSepIdx+1]
	if fstype != "proc" {
		return fmt.Errorf("mount entry with mnt_id %d for %s in %s has fstype %q, want \"proc\"; cpamp-launcher requires an unfiltered procfs (no hidepid)", wantMountID, cleanProcPath, mountInfoPath, fstype)
	}

	for _, optsCSV := range []string{matchedFields[5], matchedFields[matchedSepIdx+3]} {
		for _, opt := range strings.Split(optsCSV, ",") {
			opt = strings.TrimSpace(opt)
			if opt == "" {
				continue
			}
			if opt == "hidepid" {
				return fmt.Errorf("procfs at %s (mnt_id %d) is mounted with %s; cpamp-launcher requires an unfiltered procfs (no hidepid) for full process visibility", cleanProcPath, wantMountID, opt)
			}
			if strings.HasPrefix(opt, "hidepid=") {
				val := strings.TrimPrefix(opt, "hidepid=")
				if val != "0" && val != "off" {
					return fmt.Errorf("procfs at %s (mnt_id %d) is mounted with %s; cpamp-launcher requires an unfiltered procfs (no hidepid) for full process visibility", cleanProcPath, wantMountID, opt)
				}
			}
		}
	}
	return nil
}

// parseFDInfoMountID extracts the decimal mnt_id value from Linux
// /proc/self/fdinfo/<fd> contents.
func parseFDInfoMountID(fdInfoBytes []byte) (int, error) {
	for _, rawLine := range strings.Split(string(fdInfoBytes), "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "mnt_id:") {
			continue
		}
		valStr := strings.TrimSpace(strings.TrimPrefix(line, "mnt_id:"))
		if !isDecimalDigits(valStr) {
			return 0, fmt.Errorf("invalid mnt_id value %q in fdinfo", valStr)
		}
		mntID, err := strconv.Atoi(valStr)
		if err != nil || mntID < 0 {
			return 0, fmt.Errorf("invalid mnt_id value %q in fdinfo", valStr)
		}
		return mntID, nil
	}
	return 0, errors.New("mnt_id not found in fdinfo")
}

// ensureCPAQuiescence verifies the private PID namespace and unfiltered procfs
// mount, enumerates procPath, refuses immediately if any process mixes cpaUID
// with a protected/non-CPA UID (0, 10002, 10003), and for pure cpaUID processes
// opens a pidfd, re-verifies /proc/<pid>/status while holding the pidfd, sends
// SIGKILL through the pidfd, and repeats until two consecutive scans find no
// matching processes.
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
	selfPID, err := validatePrivatePIDNamespaceAndProcfs(procPath, sys)
	if err != nil {
		return err
	}
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
	if err != nil {
		return nil, fmt.Errorf("failed to read proc directory %s: %w", procPath, err)
	}

	var candidates []int
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
		class, uids, present, err := classifyStatusFile(statusPath, cpaUID, sys)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		switch class {
		case uidClassNoCPA:
			continue
		case uidClassMixed:
			return nil, formatMixedUIDRefusal(pid, uids)
		case uidClassPureCPA:
			candidates = append(candidates, pid)
		}
	}

	var matched []int
	for _, pid := range candidates {
		statusPath := filepath.Join(procPath, strconv.Itoa(pid), "status")
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
		// confirm the numeric PID still belongs to a pure CPA process and was
		// not reused by Manager, Ingress, root, or a mixed-UID identity.
		class, uids, present, verifyErr := classifyStatusFile(statusPath, cpaUID, sys)
		if verifyErr != nil {
			_ = handle.Close()
			return nil, verifyErr
		}
		if !present || class == uidClassNoCPA {
			_ = handle.Close()
			continue
		}
		if class == uidClassMixed {
			_ = handle.Close()
			return nil, formatMixedUIDRefusal(pid, uids)
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

func formatMixedUIDRefusal(pid int, uids [4]int) error {
	return fmt.Errorf(
		"refusing startup: process %d has mixed CPA and non-CPA UIDs [real=%d effective=%d saved=%d fs=%d]",
		pid, uids[0], uids[1], uids[2], uids[3],
	)
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

func classifyStatusFile(statusPath string, cpaUID int, sys System) (uidClassification, [4]int, bool, error) {
	var zero [4]int
	statusBytes, readErr := sys.ReadFile(statusPath)
	if errors.Is(readErr, fs.ErrNotExist) || errors.Is(readErr, syscall.ESRCH) {
		return uidClassNoCPA, zero, false, nil
	}
	if readErr != nil {
		return uidClassNoCPA, zero, false, fmt.Errorf("failed to read %s (cpamp-launcher requires an unfiltered procfs with no hidepid): %w", statusPath, readErr)
	}
	uids, ok := parseProcStatusUIDs(statusBytes)
	if !ok {
		return uidClassNoCPA, zero, false, fmt.Errorf("invalid Uid line in %s", statusPath)
	}
	return classifyProcUIDs(uids, cpaUID), uids, true, nil
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

// classifyProcUIDs classifies a process's [real, effective, saved, fs] UIDs:
//   - uidClassNoCPA: no field equals cpaUID
//   - uidClassPureCPA: every field equals cpaUID (none is 0, 10002, 10003, or any other non-CPA UID)
//   - uidClassMixed: at least one field equals cpaUID and at least one field is a non-CPA UID (such as 0, 10002, or 10003)
func classifyProcUIDs(uids [4]int, cpaUID int) uidClassification {
	hasCPA := false
	hasNonCPA := false
	for _, uid := range uids {
		if uid == cpaUID {
			hasCPA = true
		} else {
			hasNonCPA = true
		}
	}
	if !hasCPA {
		return uidClassNoCPA
	}
	if hasNonCPA {
		return uidClassMixed
	}
	return uidClassPureCPA
}
