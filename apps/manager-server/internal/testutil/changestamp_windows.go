//go:build windows

package testutil

import (
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// agedChangeTime is the instant Windows fixtures pin a file's ChangeTime to.
// It is far enough in the past that no write made by the test can reproduce it.
var agedChangeTime = time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)

// fileBasicInfo mirrors FILE_BASIC_INFORMATION. x/sys/windows exposes the
// FileBasicInfo information class but not this structure.
type fileBasicInfo struct {
	CreationTime   int64
	LastAccessTime int64
	LastWriteTime  int64
	ChangeTime     int64
	FileAttributes uint32
}

// filetimeSince1601 encodes t the way FILE_BASIC_INFORMATION stores it: 100 ns
// intervals since 1601-01-01 UTC. It matches the value copyinmarker.ChangeStamp
// formats on Windows, so a fixture can name the stamp it pinned.
func filetimeSince1601(t time.Time) int64 {
	const intervalsPerSecond = int64(10_000_000)
	const unixEpochInIntervals = int64(116_444_736_000_000_000)
	return t.UTC().Unix()*intervalsPerSecond + int64(t.UTC().Nanosecond())/100 + unixEpochInIntervals
}

// prepareDetectableRewrite pins path's FILE_BASIC_INFO timestamps into the past
// before baseline capture so origInfo, origStamp and the production detector's
// before snapshot all record agedChangeTime.
func prepareDetectableRewrite(t testing.TB, path string, stamp func(string) (string, error)) error {
	t.Helper()
	return pinChangeStamp(t, path, stamp)
}

// applyDetectableRewrite applies write once and verifies that ChangeTime moved
// away from before.
//
// Because PrepareDetectableRewrite pinned the baseline ChangeTime to
// agedChangeTime before the probe began, write() plus os.Chtimes restores
// LastWriteTime to agedChangeTime while NTFS updates ChangeTime to the current
// Windows system clock. Checking after != before verifies that precondition
// held rather than assuming it.
func applyDetectableRewrite(t testing.TB, path string, before string, write func() error, stamp func(string) (string, error)) error {
	t.Helper()
	if err := write(); err != nil {
		return err
	}
	after, err := stamp(path)
	if err != nil {
		return fmt.Errorf("read post-rewrite change stamp of %s: %w", path, err)
	}
	if after == before {
		return fmt.Errorf("change stamp of %s stayed %q across mtime-restored rewrite: ensure PrepareDetectableRewrite ran before capturing baseline", path, before)
	}
	return nil
}

// pinChangeStamp moves the file's ChangeTime and the other FILETIME members to
// a fixed instant in the past and verifies the value the detector will read.
func pinChangeStamp(t testing.TB, path string, stamp func(string) (string, error)) error {
	t.Helper()
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	handle, err := windows.CreateFile(pointer, windows.FILE_WRITE_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return fmt.Errorf("open %s to pin its change stamp: %w", path, err)
	}

	pinned := filetimeSince1601(agedChangeTime)
	basic := fileBasicInfo{
		CreationTime:   pinned,
		LastAccessTime: pinned,
		LastWriteTime:  pinned,
		ChangeTime:     pinned,
	}
	setErr := windows.SetFileInformationByHandle(handle, windows.FileBasicInfo,
		(*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic)))
	closeErr := windows.CloseHandle(handle)
	if setErr != nil {
		return fmt.Errorf("pin the change stamp of %s: %w", path, setErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close %s after pinning its change stamp: %w", path, closeErr)
	}
	// The mtime and stamp the detector reads must agree with what the fixture
	// pinned, so state the precondition instead of trusting it.
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat %s after pinning its change stamp: %w", path, err)
	}
	if got := info.ModTime().UTC(); !got.Equal(agedChangeTime) {
		return fmt.Errorf("pinned mtime = %s, want %s", got, agedChangeTime)
	}
	pinnedStamp, err := stamp(path)
	if err != nil {
		return fmt.Errorf("read the pinned change stamp of %s: %w", path, err)
	}
	if want := strconv.FormatInt(pinned, 10); pinnedStamp != want {
		return fmt.Errorf("pinned change stamp = %q, want %q", pinnedStamp, want)
	}
	return nil
}
