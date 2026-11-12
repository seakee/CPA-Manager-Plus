//go:build windows

package testutil

import (
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

// ageChangeStamp pins the file's ChangeTime and other FILETIME members to a
// fixed instant in the past. Windows records ChangeTime as file metadata, so
// every later write to the data stream reports a strictly later change stamp
// than the one pinned here.
func ageChangeStamp(t testing.TB, path string, stamp func(string) (string, error)) {
	t.Helper()
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
	handle, err := windows.CreateFile(pointer, windows.FILE_WRITE_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatalf("open %s to age its change stamp: %v", path, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	pinned := filetimeSince1601(agedChangeTime)
	basic := fileBasicInfo{
		CreationTime:   pinned,
		LastAccessTime: pinned,
		LastWriteTime:  pinned,
		ChangeTime:     pinned,
	}
	if err := windows.SetFileInformationByHandle(handle, windows.FileBasicInfo,
		(*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic))); err != nil {
		t.Fatalf("pin the change stamp of %s: %v", path, err)
	}

	// The mtime and stamp the detector reads must agree with what the fixture
	// pinned, so state the precondition instead of trusting it.
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s after aging its change stamp: %v", path, err)
	}
	if got := info.ModTime().UTC(); !got.Equal(agedChangeTime) {
		t.Fatalf("aged mtime = %s, want %s", got, agedChangeTime)
	}
	aged, err := stamp(path)
	if err != nil {
		t.Fatalf("read the aged change stamp of %s: %v", path, err)
	}
	if want := strconv.FormatInt(pinned, 10); aged != want {
		t.Fatalf("aged change stamp = %q, want %q", aged, want)
	}
}
