//go:build windows

package usage

import (
	"os"

	"golang.org/x/sys/windows"
)

func replaceArchiveFile(from, to string) error {
	fromPtr, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	toPtr, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(fromPtr, toPtr, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// Windows does not provide a portable directory fsync operation. The file is
// flushed before this point and MoveFileEx uses WRITE_THROUGH, so the directory
// sync is a compatibility no-op rather than making archive writes fail.
func syncDirectory(string) error { return nil }

// Windows FileInfo permission bits represent the DOS read-only attribute, not
// POSIX ACLs. The containing directory ACL is the authority for access control.
func archiveFilePermissionsPrivate(os.FileInfo) bool { return true }
