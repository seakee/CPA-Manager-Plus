//go:build !windows

package processlock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openLockFile(path string, flags int) (*os.File, error) {
	return os.OpenFile(path, flags, 0o600)
}

func lockFile(file *os.File) error {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return ErrLocked
	}
	return err
}

// probeLockFile takes a shared, non-blocking lock. It conflicts with a live
// exclusive holder without excluding another reader, so an observer can name a
// busy source and let it go immediately.
func probeLockFile(file *os.File) error {
	err := unix.Flock(int(file.Fd()), unix.LOCK_SH|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return ErrLocked
	}
	return err
}

func unlockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}

func hasMultipleLinks(file *os.File) (bool, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return false, err
	}
	return stat.Nlink > 1, nil
}
