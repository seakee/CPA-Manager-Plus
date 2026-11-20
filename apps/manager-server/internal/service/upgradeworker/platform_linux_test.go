//go:build linux

package upgradeworker

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRepairTargetOwnershipFDRepairsManagerLockAndSkipsRootAndRuntime(t *testing.T) {
	targetRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve target root: %v", err)
	}
	for _, rel := range []string{
		"usage.sqlite",
		"usage.sqlite-wal",
		"usage.sqlite-shm",
		"usage.sqlite.manager.lock",
		"data.key",
		"runtime-secret",
	} {
		if err := os.WriteFile(filepath.Join(targetRoot, rel), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(targetRoot, "usage-archives"), 0o750); err != nil {
		t.Fatalf("mkdir archives: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetRoot, "usage-archives", "seg.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	var chowned []string
	if err := repairTargetOwnershipFD(targetRoot, func(fd int, relPath string) error {
		if fd < 0 {
			t.Fatalf("invalid descriptor for %s", relPath)
		}
		chowned = append(chowned, filepath.ToSlash(relPath))
		return nil
	}); err != nil {
		t.Fatalf("repairTargetOwnershipFD: %v", err)
	}
	slices.Sort(chowned)
	want := []string{
		"data.key",
		"usage-archives",
		"usage-archives/seg.jsonl",
		"usage.sqlite",
		"usage.sqlite-shm",
		"usage.sqlite-wal",
		"usage.sqlite.manager.lock",
	}
	if !slices.Equal(chowned, want) {
		t.Fatalf("chowned = %v, want %v", chowned, want)
	}
}

func TestRepairRefusesDirectoryForFileOnlyMember(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "data.key"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := repairTargetOwnershipFD(root, func(_ int, path string) error {
		t.Fatalf("unexpected ownership repair: %s", path)
		return nil
	})
	if !errors.Is(err, errTargetOwnershipUnsupported) {
		t.Fatalf("repair error = %v, want unsupported file type", err)
	}
}

func TestRepairOwnershipChangesOnlyManagerFiles(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires the Linux worker's root permissions")
	}
	root := t.TempDir()
	for _, name := range []string{"usage.sqlite", "runtime-secret"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := RepairTargetOwnership(root); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		path     string
		uid, gid uint32
	}{{root, 0, uint32(os.Getegid())}, {filepath.Join(root, "usage.sqlite"), ManagerUID, ManagerGID}, {filepath.Join(root, "runtime-secret"), 0, uint32(os.Getegid())}} {
		info, err := os.Stat(check.path)
		if err != nil {
			t.Fatal(err)
		}
		stat := info.Sys().(*syscall.Stat_t)
		if stat.Uid != check.uid || stat.Gid != check.gid {
			t.Fatalf("%s owned by %d:%d, want %d:%d", check.path, stat.Uid, stat.Gid, check.uid, check.gid)
		}
	}
}

func TestIsLauncherProcessAtAcceptsPID1LauncherAndRefusesHostInit(t *testing.T) {
	procRoot := t.TempDir()
	pid1Dir := filepath.Join(procRoot, "1")
	if err := os.MkdirAll(pid1Dir, 0o755); err != nil {
		t.Fatalf("mkdir proc/1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pid1Dir, "comm"), []byte("systemd\n"), 0o644); err != nil {
		t.Fatalf("write systemd comm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pid1Dir, "cmdline"), []byte("/sbin/init\x00"), 0o644); err != nil {
		t.Fatalf("write init cmdline: %v", err)
	}
	if isLauncherProcessAt(procRoot, 1) {
		t.Fatal("expected systemd/init PID 1 to be refused")
	}

	if err := os.WriteFile(filepath.Join(pid1Dir, "comm"), []byte("cpamp-launcher\n"), 0o644); err != nil {
		t.Fatalf("write launcher comm: %v", err)
	}
	if !isLauncherProcessAt(procRoot, 1) {
		t.Fatal("expected cpamp-launcher PID 1 to be accepted")
	}
}

func TestProcStartTimeTicksAtParsesCommWithSpacesAndParens(t *testing.T) {
	procRoot := t.TempDir()
	pidDir := filepath.Join(procRoot, "1")
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		t.Fatalf("mkdir proc/1: %v", err)
	}
	// Field 1: pid, Field 2: (comm with spaces and ) paren), Field 3..21: 19 tokens, Field 22: starttime 987654.
	statLine := "1 (cpamp ) launcher) S 0 1 1 0 -1 4194560 100 0 0 0 10 5 0 0 20 0 4 0 987654 12345678 200\n"
	if err := os.WriteFile(filepath.Join(pidDir, "stat"), []byte(statLine), 0o644); err != nil {
		t.Fatalf("write stat: %v", err)
	}
	ticks, err := procStartTimeTicksAt(procRoot, 1)
	if err != nil {
		t.Fatalf("procStartTimeTicksAt: %v", err)
	}
	if ticks != "987654" {
		t.Fatalf("ticks = %q, want 987654", ticks)
	}
}

func TestBindLauncherParentRestartEnforcesLauncherIdentityForAllPIDsAndExplicitResignal(t *testing.T) {
	// 1. Non-launcher parent with PID > 1 (e.g. shell or docker exec) must fail closed.
	if _, err := bindLauncherParentRestartHooks(
		4242,
		10*time.Millisecond,
		func() int { return 4242 },
		func(int) (string, error) { return "777001", nil },
		func(int) bool { return false },
		func() error { t.Fatal("unexpected signal for non-launcher parent"); return nil },
		nil,
	); err == nil {
		t.Fatal("expected non-launcher parent PID > 1 to be refused")
	}

	// A trusted launcher can receive an explicit re-signal only while its
	// startup identity holds; delivery is never retried automatically.
	var currentTicks atomic.Value
	currentTicks.Store("777001")
	var signals atomic.Int32
	signalCh := make(chan struct{}, 4)

	pr, err := bindLauncherParentRestartHooks(
		4242,
		25*time.Millisecond,
		func() int { return 4242 },
		func(int) (string, error) { return currentTicks.Load().(string), nil },
		func(pid int) bool { return pid == 4242 },
		func() error {
			signals.Add(1)
			signalCh <- struct{}{}
			return nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("bindLauncherParentRestartHooks: %v", err)
	}
	defer pr.Close()

	if err := pr.Restart(); err != nil {
		t.Fatalf("first Restart: %v", err)
	}
	select {
	case <-signalCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first delayed signal")
	}
	time.Sleep(60 * time.Millisecond)
	if got := signals.Load(); got != 1 {
		t.Fatalf("signals after single drain window = %d, want 1 (no auto-retry)", got)
	}

	// Explicit retry after prior timer completed re-delivers SIGTERM to same parent.
	if err := pr.Restart(); err != nil {
		t.Fatalf("explicit second Restart: %v", err)
	}
	select {
	case <-signalCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for explicit second signal")
	}
	if got := signals.Load(); got != 2 {
		t.Fatalf("signals after explicit retry = %d, want 2", got)
	}

	// Parent start-time drift fails closed without signaling.
	currentTicks.Store("999999")
	if err := pr.Validate(); err == nil {
		t.Fatal("expected Validate error after parent start time changed")
	}
	if err := pr.Restart(); err == nil {
		t.Fatal("expected Restart error after parent start time changed")
	}
	if got := signals.Load(); got != 2 {
		t.Fatalf("signals after parent drift = %d, want 2", got)
	}
}

func TestParentRestartReturnsDeliveryFailure(t *testing.T) {
	sendErr := errors.New("signal delivery unavailable")
	pr, err := bindLauncherParentRestartHooks(1, time.Millisecond, func() int { return 1 },
		func(int) (string, error) { return "100", nil }, func(int) bool { return true },
		func() error { return sendErr }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	if err := pr.Restart(); !errors.Is(err, sendErr) {
		t.Fatalf("restart = %v, want signal delivery error", err)
	}
}

func TestChownMemberAtEnforcesMountAndDepthBounds(t *testing.T) {
	targetRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve target root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetRoot, "usage.sqlite"), []byte("db"), 0o600); err != nil {
		t.Fatalf("write usage.sqlite: %v", err)
	}
	rootFD, err := unix.Open(targetRoot, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open rootFD: %v", err)
	}
	defer unix.Close(rootFD)
	var rootStat unix.Stat_t
	if err := unix.Fstat(rootFD, &rootStat); err != nil {
		t.Fatalf("fstat rootFD: %v", err)
	}
	if err := chownMemberAt(rootFD, "usage.sqlite", "usage.sqlite", uint64(rootStat.Dev)^1, 1, func(int, string) error {
		t.Fatal("unexpected chown on cross-mount entry")
		return nil
	}); !errors.Is(err, errTargetOwnershipCrossMount) {
		t.Fatalf("cross-mount error = %v, want %v", err, errTargetOwnershipCrossMount)
	}

	deep := filepath.Join(targetRoot, "usage-archives")
	for i := range maxTargetOwnershipDepth {
		deep = filepath.Join(deep, "d"+strconv.Itoa(i))
	}
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatalf("mkdir deep archives: %v", err)
	}
	if err := repairTargetOwnershipFD(targetRoot, func(_ int, relPath string) error {
		if parts := len(strings.Split(filepath.ToSlash(relPath), "/")); parts > maxTargetOwnershipDepth {
			t.Fatalf("unexpected chown past depth bound: %s", relPath)
		}
		return nil
	}); !errors.Is(err, errTargetOwnershipUnsupported) {
		t.Fatalf("deep repairTargetOwnershipFD error = %v, want %v", err, errTargetOwnershipUnsupported)
	}
}

func TestServeHTTPOnListenerReturnsImmediatelyWhenListenerFails(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- serveHTTPOnListener(context.Background(), listener, &http.Server{Handler: http.NotFoundHandler()})
	}()
	select {
	case serveErr := <-done:
		if serveErr == nil {
			t.Fatal("expected closed listener error, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serveHTTPOnListener hung waiting on ctx.Done() after listener error")
	}
}

func TestOpenParentPidfdAllowsENOSYSAndEPERMFallbackAndFailsClosedOtherwise(t *testing.T) {
	for _, fallbackErr := range []error{unix.ENOSYS, unix.EPERM} {
		fd, err := openParentPidfd(1, func(int, int) (int, error) {
			return -1, fallbackErr
		})
		if err != nil || fd != -1 {
			t.Fatalf("openParentPidfd(%v) = (%d, %v), want (-1, nil)", fallbackErr, fd, err)
		}
	}
	for _, pidfdErr := range []error{unix.ESRCH, unix.EINVAL, unix.EMFILE} {
		if _, err := openParentPidfd(1, func(int, int) (int, error) {
			return -1, pidfdErr
		}); !errors.Is(err, pidfdErr) {
			t.Fatalf("openParentPidfd(%v) = %v, want error wrapping %v", pidfdErr, err, pidfdErr)
		}
	}
	f, err := os.CreateTemp(t.TempDir(), "pidfd-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	dupFD, err := unix.Dup(int(f.Fd()))
	_ = f.Close()
	if err != nil {
		t.Fatalf("dup fd: %v", err)
	}
	defer unix.Close(dupFD)
	gotFD, err := openParentPidfd(1, func(int, int) (int, error) {
		return dupFD, nil
	})
	if err != nil || gotFD != dupFD {
		t.Fatalf("valid openParentPidfd = (%d, %v), want (%d, nil)", gotFD, err, dupFD)
	}
	flags, err := unix.FcntlInt(uintptr(gotFD), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("pidfd FD_CLOEXEC flags = %d (err=%v), want FD_CLOEXEC set", flags, err)
	}
}

func TestParentRestartPidfdSendSignalFallbackAndFailClosed(t *testing.T) {
	openTempFD := func(t *testing.T) int {
		t.Helper()
		f, err := os.CreateTemp(t.TempDir(), "pidfd-restart-*")
		if err != nil {
			t.Fatalf("create temp file: %v", err)
		}
		dupFD, err := unix.Dup(int(f.Fd()))
		_ = f.Close()
		if err != nil {
			t.Fatalf("dup fd: %v", err)
		}
		return dupFD
	}

	for _, fallbackErr := range []error{unix.ENOSYS, unix.EPERM} {
		var kills atomic.Int32
		dupFD := openTempFD(t)
		pr, err := bindLauncherParentRestartWithPidfd(
			77,
			0,
			func() int { return 77 },
			func(int) (string, error) { return "55555", nil },
			func(int) bool { return true },
			func(int, int) (int, error) { return dupFD, nil },
			func(int, unix.Signal) error { return fallbackErr },
			func(pid int, sig unix.Signal) error {
				if pid != 77 || sig != unix.SIGTERM {
					t.Fatalf("unexpected kill(%d, %v)", pid, sig)
				}
				kills.Add(1)
				return nil
			},
			unix.Close,
		)
		if err != nil {
			t.Fatalf("bind (%v): %v", fallbackErr, err)
		}
		if err := pr.Restart(); err != nil {
			pr.Close()
			t.Fatalf("Restart with %v fallback = %v, want nil", fallbackErr, err)
		}
		pr.Close()
		if got := kills.Load(); got != 1 {
			t.Fatalf("kill fallback count for %v = %d, want 1", fallbackErr, got)
		}
	}

	// Re-validation before kill(2) fails closed if parent identity drifted during pidfd_send_signal.
	var driftedTicks atomic.Value
	driftedTicks.Store("55555")
	var driftedKills atomic.Int32
	dupFD := openTempFD(t)
	prDrift, err := bindLauncherParentRestartWithPidfd(
		77,
		0,
		func() int { return 77 },
		func(int) (string, error) { return driftedTicks.Load().(string), nil },
		func(int) bool { return true },
		func(int, int) (int, error) { return dupFD, nil },
		func(int, unix.Signal) error {
			driftedTicks.Store("99999")
			return unix.EPERM
		},
		func(int, unix.Signal) error {
			driftedKills.Add(1)
			return nil
		},
		unix.Close,
	)
	if err != nil {
		t.Fatalf("bind drift case: %v", err)
	}
	if err := prDrift.Restart(); err == nil {
		prDrift.Close()
		t.Fatal("expected Restart error when parent drifted before kill fallback")
	}
	prDrift.Close()
	if got := driftedKills.Load(); got != 0 {
		t.Fatalf("kills after parent drift = %d, want 0", got)
	}

	// Non-ENOSYS/EPERM errors (ESRCH, EINVAL) fail closed without calling kill(2).
	for _, hardErr := range []error{unix.ESRCH, unix.EINVAL} {
		var kills atomic.Int32
		hardFD := openTempFD(t)
		prHard, err := bindLauncherParentRestartWithPidfd(
			77,
			0,
			func() int { return 77 },
			func(int) (string, error) { return "55555", nil },
			func(int) bool { return true },
			func(int, int) (int, error) { return hardFD, nil },
			func(int, unix.Signal) error { return hardErr },
			func(int, unix.Signal) error {
				kills.Add(1)
				return nil
			},
			unix.Close,
		)
		if err != nil {
			t.Fatalf("bind (%v): %v", hardErr, err)
		}
		if err := prHard.Restart(); !errors.Is(err, hardErr) {
			prHard.Close()
			t.Fatalf("Restart with %v = %v, want %v", hardErr, err, hardErr)
		}
		prHard.Close()
		if got := kills.Load(); got != 0 {
			t.Fatalf("kills for %v = %d, want 0", hardErr, got)
		}
	}
}

func TestIsLauncherProcessAtUsesReadableExeAuthoritatively(t *testing.T) {
	procRoot := t.TempDir()

	nonLauncherDir := filepath.Join(procRoot, "42")
	if err := os.MkdirAll(nonLauncherDir, 0o755); err != nil {
		t.Fatalf("mkdir proc/42: %v", err)
	}
	if err := os.Symlink("/bin/sh", filepath.Join(nonLauncherDir, "exe")); err != nil {
		t.Fatalf("symlink non-launcher exe: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nonLauncherDir, "comm"), []byte("cpamp-launcher\n"), 0o644); err != nil {
		t.Fatalf("write comm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nonLauncherDir, "cmdline"), []byte("cpamp-launcher\x00"), 0o644); err != nil {
		t.Fatalf("write cmdline: %v", err)
	}
	if isLauncherProcessAt(procRoot, 42) {
		t.Fatal("expected readable non-launcher exe to fail closed without falling back to comm/cmdline")
	}

	launcherDir := filepath.Join(procRoot, "43")
	if err := os.MkdirAll(launcherDir, 0o755); err != nil {
		t.Fatalf("mkdir proc/43: %v", err)
	}
	if err := os.Symlink("/usr/local/bin/cpamp-launcher", filepath.Join(launcherDir, "exe")); err != nil {
		t.Fatalf("symlink launcher exe: %v", err)
	}
	if err := os.WriteFile(filepath.Join(launcherDir, "comm"), []byte("other\n"), 0o644); err != nil {
		t.Fatalf("write other comm: %v", err)
	}
	if !isLauncherProcessAt(procRoot, 43) {
		t.Fatal("expected readable cpamp-launcher exe to succeed")
	}
}
