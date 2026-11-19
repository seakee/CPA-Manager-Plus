//go:build linux

package upgradeworker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type peerState struct {
	enforced bool
	allowed  bool
}

type peerContextKey struct{}

func fileLinkCount(info os.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat != nil {
		return uint64(stat.Nlink)
	}
	return 1
}

func peerAllowed(ctx context.Context) bool {
	state, ok := ctx.Value(peerContextKey{}).(peerState)
	if !ok || !state.enforced {
		return true
	}
	return state.allowed
}

func withSocketPeerEnforcement(ctx context.Context, allowed bool) context.Context {
	return context.WithValue(ctx, peerContextKey{}, peerState{enforced: true, allowed: allowed})
}

func connectionContext(ctx context.Context, conn net.Conn) context.Context {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return withSocketPeerEnforcement(ctx, false)
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return withSocketPeerEnforcement(ctx, false)
	}
	var (
		peer    *unix.Ucred
		credErr error
	)
	if err := raw.Control(func(fd uintptr) {
		peer, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || credErr != nil || peer == nil {
		return withSocketPeerEnforcement(ctx, false)
	}
	allowed := peer.Pid > 0 && (peer.Uid == 0 || peer.Uid == ManagerUID)
	return withSocketPeerEnforcement(ctx, allowed)
}

func isArtifactUploadPut(r *http.Request) bool {
	if r == nil || r.URL == nil || r.Method != http.MethodPut {
		return false
	}
	path := r.URL.Path
	const suffix = "/installation/upgrade/runtime/artifact/upload"
	return strings.HasSuffix(path, suffix) && path != suffix
}

func Serve(ctx context.Context, socketPath string, handler http.Handler) error {
	if os.Geteuid() != 0 {
		return errors.New("upgrade_worker_requires_root")
	}
	cleaned := filepath.Clean(strings.TrimSpace(socketPath))
	if !filepath.IsAbs(cleaned) {
		return errors.New("upgrade_worker_socket_invalid")
	}
	dir := filepath.Dir(cleaned)
	if err := ensurePrivateDir(dir); err != nil {
		return err
	}
	if info, err := os.Lstat(cleaned); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("upgrade_worker_socket_occupied")
		}
		if err := os.Remove(cleaned); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	oldMask := unix.Umask(0o177)
	listener, err := net.Listen("unix", cleaned)
	unix.Umask(oldMask)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(cleaned)
	}()
	if err := os.Chown(cleaned, 0, ManagerGID); err != nil {
		return err
	}
	if err := os.Chmod(cleaned, 0o660); err != nil {
		return err
	}
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if state, ok := r.Context().Value(peerContextKey{}).(peerState); !ok || !state.enforced || !state.allowed {
			writeError(w, http.StatusForbidden, "upgrade_worker_peer_refused", nil)
			return
		}
		deadline := 40 * time.Second
		if isArtifactUploadPut(r) {
			deadline = 16 * time.Minute
		}
		rc := http.NewResponseController(w)
		now := time.Now()
		_ = rc.SetReadDeadline(now.Add(deadline))
		_ = rc.SetWriteDeadline(now.Add(deadline))
		handler.ServeHTTP(w, r)
	})
	server := &http.Server{
		Handler:           wrapped,
		ConnContext:       connectionContext,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	return serveHTTPOnListener(ctx, listener, server)
}

func serveHTTPOnListener(ctx context.Context, listener net.Listener, server *http.Server) error {
	serveDone := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				_ = server.Close()
			}
		case <-serveDone:
			return
		}
	}()
	err := server.Serve(listener)
	close(serveDone)
	<-shutdownDone
	if errors.Is(err, http.ErrServerClosed) && ctx.Err() != nil {
		return nil
	}
	return err
}

func ensurePrivateDir(dir string) error {
	parent := filepath.Dir(dir)
	if info, err := os.Lstat(parent); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("upgrade_worker_socket_parent_invalid")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("upgrade_worker_socket_dir_invalid")
	}
	if err := os.Chown(dir, 0, ManagerGID); err != nil {
		return err
	}
	return os.Chmod(dir, 0o750)
}

// RepairTargetOwnership hands Manager-owned files inside targetRoot (/data) to
// ManagerUID:ManagerGID using O_NOFOLLOW descriptors without touching
// targetRoot itself (preserving launcher 0:10002 sticky 01771), runtime-secret,
// or /runtime.
func RepairTargetOwnership(targetRoot string) error {
	return repairTargetOwnershipFD(targetRoot, func(fd int, _ string) error {
		return unix.Fchownat(fd, "", ManagerUID, ManagerGID, unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW)
	})
}

func repairTargetOwnershipFD(targetRoot string, chownFD func(fd int, relPath string) error) error {
	cleaned := filepath.Clean(strings.TrimSpace(targetRoot))
	if !filepath.IsAbs(cleaned) || cleaned == "/" {
		return errTargetOwnershipRootInvalid
	}
	if cleaned == "/runtime" || strings.HasPrefix(cleaned, "/runtime/") {
		return errTargetOwnershipRuntimeDenied
	}
	const dirFlags = unix.O_PATH | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	rootFD, err := unix.Open("/", dirFlags, 0)
	if err != nil {
		return err
	}
	for _, part := range strings.Split(strings.TrimPrefix(cleaned, "/"), "/") {
		next, openErr := unix.Openat(rootFD, part, dirFlags, 0)
		_ = unix.Close(rootFD)
		if openErr != nil {
			return openErr
		}
		rootFD = next
	}
	defer unix.Close(rootFD)
	var rootStat unix.Stat_t
	if err := unix.Fstat(rootFD, &rootStat); err != nil {
		return err
	}
	expectedDev := uint64(rootStat.Dev)
	for _, name := range targetOwnedMembers {
		if isExcludedTargetMember(name) {
			return errTargetOwnershipRuntimeDenied
		}
		if err := chownMemberAt(rootFD, name, name, expectedDev, 1, chownFD); err != nil {
			return err
		}
	}
	return nil
}

func chownMemberAt(parentFD int, name, relPath string, expectedDev uint64, depth int, chownFD func(fd int, relPath string) error) error {
	if isExcludedTargetMember(name) || strings.ContainsRune(name, '/') {
		return errTargetOwnershipRuntimeDenied
	}
	if depth > maxTargetOwnershipDepth {
		return errTargetOwnershipUnsupported
	}
	fd, err := unix.Openat(parentFD, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	mode := stat.Mode & unix.S_IFMT
	if mode == unix.S_IFLNK {
		return errTargetOwnershipSymlink
	}
	if uint64(stat.Dev) != expectedDev {
		return errTargetOwnershipCrossMount
	}
	if mode != unix.S_IFREG && mode != unix.S_IFDIR {
		return errTargetOwnershipUnsupported
	}
	if relPath == name {
		isWorkDirectory := name == "usage-archives" || name == ".upgrade-copy-in"
		if isWorkDirectory != (mode == unix.S_IFDIR) {
			return errTargetOwnershipUnsupported
		}
	}
	if mode == unix.S_IFREG && uint64(stat.Nlink) > 1 {
		return errTargetOwnershipHardlink
	}
	if chownFD != nil {
		if err := chownFD(fd, relPath); err != nil {
			return err
		}
	}
	if mode != unix.S_IFDIR {
		return nil
	}
	listing, err := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(listing), name)
	entries, readErr := file.Readdirnames(-1)
	_ = file.Close()
	if readErr != nil {
		return readErr
	}
	for _, child := range entries {
		if isExcludedTargetMember(child) {
			return errTargetOwnershipRuntimeDenied
		}
		if err := chownMemberAt(fd, child, filepath.Join(relPath, child), expectedDev, depth+1, chownFD); err != nil {
			return err
		}
	}
	return nil
}

// ParentRestart holds the validated, single-parent restart callbacks bound at
// worker startup for --restart-parent.
type ParentRestart struct {
	Validate func() error
	Restart  func() error
	Close    func()
}

// BindLauncherParentRestart binds controlled SIGTERM delivery to the worker's
// own launcher parent without accepting any caller-supplied PID or signal.
// Every parent PID (PID 1 or > 1) must prove it is cpamp-launcher; host init,
// shells, docker exec, or arbitrary supervisors are refused fail-closed.
func BindLauncherParentRestart(delay time.Duration) (ParentRestart, error) {
	parent := os.Getppid()
	if parent < 1 {
		return ParentRestart{}, errors.New("upgrade worker: --restart-parent requires a valid launcher parent")
	}
	var procStat unix.Stat_t
	if err := unix.Stat(fmt.Sprintf("/proc/%d", parent), &procStat); err != nil {
		return ParentRestart{}, fmt.Errorf("upgrade worker: inspect parent %d: %w", parent, err)
	}
	if procStat.Uid != uint32(os.Geteuid()) {
		return ParentRestart{}, errors.New("upgrade worker: --restart-parent parent UID mismatch")
	}
	return bindLauncherParentRestartWithPidfd(
		parent,
		delay,
		os.Getppid,
		procStartTimeTicks,
		isTrustedLauncherPID,
		unix.PidfdOpen,
		func(fd int, sig unix.Signal) error {
			return unix.PidfdSendSignal(fd, sig, nil, 0)
		},
		unix.Kill,
		unix.Close,
	)
}

func bindLauncherParentRestartWithPidfd(
	parent int,
	delay time.Duration,
	currentPPID func() int,
	readStartTicks func(int) (string, error),
	trustedLauncher func(int) bool,
	openFn func(pid int, flags int) (int, error),
	pidfdSignalFn func(pidfd int, sig unix.Signal) error,
	killFn func(pid int, sig unix.Signal) error,
	closeFn func(fd int) error,
) (ParentRestart, error) {
	pidfd, pidfdErr := openParentPidfd(parent, openFn)
	if pidfdErr != nil {
		return ParentRestart{}, pidfdErr
	}
	validate := func() error {
		return nil
	}
	var bound ParentRestart
	bound, err := bindLauncherParentRestartHooks(
		parent,
		delay,
		currentPPID,
		readStartTicks,
		trustedLauncher,
		func() error {
			if pidfd >= 0 {
				err := pidfdSignalFn(pidfd, unix.SIGTERM)
				if err == nil {
					return nil
				}
				if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EPERM) {
					return err
				}
				if valErr := validate(); valErr != nil {
					return valErr
				}
			}
			// Older kernels or seccomp profiles blocking pidfd_open/pidfd_send_signal
			// re-verify parent identity before falling back to kill(2).
			return killFn(parent, unix.SIGTERM)
		},
		func() {
			if pidfd >= 0 {
				if closeFn != nil {
					_ = closeFn(pidfd)
				}
				pidfd = -1
			}
		},
	)
	if err != nil {
		if pidfd >= 0 && closeFn != nil {
			_ = closeFn(pidfd)
		}
		return ParentRestart{}, err
	}
	validate = bound.Validate
	return bound, nil
}

func openParentPidfd(parent int, openFn func(pid int, flags int) (int, error)) (int, error) {
	pidfd, err := openFn(parent, 0)
	if err != nil {
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) {
			return -1, nil
		}
		return -1, fmt.Errorf("upgrade worker: open parent pidfd %d: %w", parent, err)
	}
	unix.CloseOnExec(pidfd)
	return pidfd, nil
}

func bindLauncherParentRestartHooks(
	parent int,
	delay time.Duration,
	currentPPID func() int,
	readStartTicks func(int) (string, error),
	trustedLauncher func(int) bool,
	sendSignal func() error,
	closeHandle func(),
) (ParentRestart, error) {
	if parent < 1 {
		return ParentRestart{}, errors.New("upgrade worker: --restart-parent requires a valid launcher parent")
	}
	if !trustedLauncher(parent) {
		return ParentRestart{}, fmt.Errorf("upgrade worker: --restart-parent refuses non-launcher parent PID %d", parent)
	}
	startTicks, err := readStartTicks(parent)
	if err != nil {
		return ParentRestart{}, fmt.Errorf("upgrade worker: read parent start time: %w", err)
	}

	var (
		mu       sync.Mutex
		inFlight bool
		closed   bool
	)
	validate := func() error {
		if currentPPID() != parent {
			return errors.New("upgrade_restart_parent_changed")
		}
		currentTicks, err := readStartTicks(parent)
		if err != nil || currentTicks != startTicks {
			return errors.New("upgrade_restart_parent_changed")
		}
		if !trustedLauncher(parent) {
			return errors.New("upgrade_restart_parent_invalid")
		}
		return nil
	}
	deliver := func() error {
		mu.Lock()
		defer mu.Unlock()
		inFlight = false
		if closed {
			if closeHandle != nil {
				closeHandle()
			}
			return errors.New("upgrade_restart_closed")
		}
		if err := validate(); err != nil {
			return err
		}
		if sendSignal == nil {
			return errors.New("upgrade_restart_disabled")
		}
		return sendSignal()
	}
	restart := func() error {
		if err := validate(); err != nil {
			return err
		}
		mu.Lock()
		if closed {
			mu.Unlock()
			return errors.New("upgrade_restart_closed")
		}
		if inFlight {
			mu.Unlock()
			return nil
		}
		inFlight = true
		mu.Unlock()
		// The HTTP handler already flushed the response. Finish delivery here
		// so the worker can publish failure instead of silently losing it.
		if delay > 0 {
			time.Sleep(delay)
		}
		return deliver()
	}
	cleanup := func() {
		mu.Lock()
		defer mu.Unlock()
		closed = true
		if !inFlight && closeHandle != nil {
			closeHandle()
		}
	}
	return ParentRestart{
		Validate: validate,
		Restart:  restart,
		Close:    cleanup,
	}, nil
}

func isTrustedLauncherPID(pid int) bool {
	return isLauncherProcessAt("/proc", pid)
}

func isLauncherProcessAt(procRoot string, pid int) bool {
	base := filepath.Join(procRoot, fmt.Sprintf("%d", pid))
	if exe, err := os.Readlink(filepath.Join(base, "exe")); err == nil {
		exe = strings.TrimSuffix(strings.TrimSpace(exe), " (deleted)")
		return filepath.Base(exe) == "cpamp-launcher"
	}
	if comm, err := os.ReadFile(filepath.Join(base, "comm")); err == nil {
		if strings.TrimSpace(string(comm)) == "cpamp-launcher" {
			return true
		}
	}
	if cmdline, err := os.ReadFile(filepath.Join(base, "cmdline")); err == nil && len(cmdline) > 0 {
		arg0 := cmdline
		if idx := bytes.IndexByte(cmdline, 0); idx >= 0 {
			arg0 = cmdline[:idx]
		}
		if filepath.Base(strings.TrimSpace(string(arg0))) == "cpamp-launcher" {
			return true
		}
	}
	return false
}

func procStartTimeTicks(pid int) (string, error) {
	return procStartTimeTicksAt("/proc", pid)
}

func procStartTimeTicksAt(procRoot string, pid int) (string, error) {
	raw, err := os.ReadFile(filepath.Join(procRoot, fmt.Sprintf("%d", pid), "stat"))
	if err != nil {
		return "", err
	}
	text := string(raw)
	endComm := strings.LastIndexByte(text, ')')
	if endComm < 0 || endComm+1 >= len(text) {
		return "", errors.New("malformed proc stat")
	}
	fields := strings.Fields(text[endComm+1:])
	if len(fields) <= 19 || fields[19] == "" {
		return "", errors.New("missing proc starttime")
	}
	return fields[19], nil
}
