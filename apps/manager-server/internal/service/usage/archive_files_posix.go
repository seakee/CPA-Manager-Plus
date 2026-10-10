//go:build !windows

package usage

import (
	"fmt"
	"os"
	"reflect"
)

// POSIX ctime detects in-place edits even if size and mtime are restored. Stat_t
// names the timestamp differently across the supported Linux and BSD/macOS ABIs.
func archiveFileChangeStamp(_ *os.File, info os.FileInfo) (string, error) {
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
	return "", fmt.Errorf("archive file change time is unavailable")
}

func replaceArchiveFile(from, to string) error {
	return os.Rename(from, to)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func archiveFilePermissionsPrivate(info os.FileInfo) bool {
	return info.Mode().Perm()&0o077 == 0
}
