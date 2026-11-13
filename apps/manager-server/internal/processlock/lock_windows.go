//go:build windows

package processlock

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// Share deletion so a failed Fresh admission can remove its own marker before
// releasing the fence. Every opener validates file identity after locking.
func openLockFile(path string, flags int) (*os.File, error) {
	// Callers supply canonical absolute paths. Preserve os.OpenFile's support
	// for long paths even on hosts without the long-path registry opt-in.
	apiPath := path
	if len(path) >= 248 && !strings.HasPrefix(path, `\\?\`) && !strings.HasPrefix(path, `\??\`) && !strings.HasPrefix(path, `\\.\`) {
		if strings.HasPrefix(path, `\\`) {
			apiPath = `\\?\UNC\` + path[2:]
		} else {
			apiPath = `\\?\` + path
		}
	}
	name, err := windows.UTF16PtrFromString(apiPath)
	if err != nil {
		return nil, err
	}
	access := uint32(windows.GENERIC_READ)
	if flags&os.O_RDWR != 0 {
		access |= windows.GENERIC_WRITE
	}
	creation := uint32(windows.OPEN_EXISTING)
	if flags&os.O_CREATE != 0 {
		creation = windows.OPEN_ALWAYS
		if flags&os.O_EXCL != 0 {
			creation = windows.CREATE_NEW
		}
	}
	handle, err := windows.CreateFile(name, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, creation, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

func tightenLockFile(*os.File) error {
	return nil
}

func lockFile(file *os.File) error {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrLocked
	}
	return err
}

// lockFileShared holds a shared, non-blocking lock for as long as the handle
// stays open. It excludes every exclusive holder and admits other readers, so
// concurrent read-only observations of one stopped source share the fence
// instead of refusing each other.
func lockFileShared(file *os.File) error {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrLocked
	}
	return err
}

func unlockFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}

// probeLockFile takes a shared, non-blocking lock. It conflicts with a live
// exclusive holder without excluding another reader, so an observer can name a
// busy source and let it go immediately.
func probeLockFile(file *os.File) error {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrLocked
	}
	return err
}

func hasMultipleLinks(file *os.File) (bool, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return false, err
	}
	return info.NumberOfLinks > 1, nil
}

func hasMultipleLinksPath(path string) (bool, error) {
	apiPath := path
	if len(path) >= 248 && !strings.HasPrefix(path, `\\?\`) && !strings.HasPrefix(path, `\??\`) && !strings.HasPrefix(path, `\\.\`) {
		if strings.HasPrefix(path, `\\`) {
			apiPath = `\\?\UNC\` + path[2:]
		} else {
			apiPath = `\\?\` + path
		}
	}
	name, err := windows.UTF16PtrFromString(apiPath)
	if err != nil {
		return false, err
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return false, &os.PathError{Op: "lstat", Path: path, Err: err}
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return false, err
	}
	return info.NumberOfLinks > 1, nil
}
