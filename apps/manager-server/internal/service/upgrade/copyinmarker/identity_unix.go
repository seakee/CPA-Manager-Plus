//go:build !windows

package copyinmarker

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"syscall"
)

// ErrChangeStampUnavailable is returned when the platform cannot report a
// file's metadata change timestamp (ctime / ChangeTime).
var ErrChangeStampUnavailable = errors.New("file change time is unavailable")

// identity reports the device and inode the filesystem assigned the file. Both
// survive a rename and an in-place rewrite, which is what the marker needs: the
// migration rewrites the database at the same path, and only a swapped-in file
// changes its inode.
func identity(path string) (FileIdentity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileIdentity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return FileIdentity{}, errors.New("the platform reports no file identity")
	}
	return FileIdentity{Device: uint64(stat.Dev), Inode: uint64(stat.Ino)}, nil
}

// ChangeStamp reports the POSIX ctime of a file or directory without following
// symlinks, matching internal/service/usage/archive_files_posix.go so an
// in-place rewrite with restored mtime still changes the stamp.
func ChangeStamp(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	stat := reflect.ValueOf(info.Sys())
	if stat.Kind() == reflect.Pointer && !stat.IsNil() {
		stat = stat.Elem()
	}
	if stat.Kind() == reflect.Struct {
		for _, name := range []string{"Ctim", "Ctimespec"} {
			if stamp := stat.FieldByName(name); stamp.IsValid() && stamp.CanInterface() {
				return fmt.Sprint(stamp.Interface()), nil
			}
		}
		if seconds, nanos := stat.FieldByName("Ctime"), stat.FieldByName("Ctimensec"); seconds.IsValid() && nanos.IsValid() && seconds.CanInterface() && nanos.CanInterface() {
			return fmt.Sprint(seconds.Interface(), ":", nanos.Interface()), nil
		}
	}
	return "", ErrChangeStampUnavailable
}
