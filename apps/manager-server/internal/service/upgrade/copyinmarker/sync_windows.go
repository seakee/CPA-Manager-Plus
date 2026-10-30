//go:build windows

package copyinmarker

import "golang.org/x/sys/windows"

// Windows has no portable directory fsync. File FlushFileBuffers and a
// same-volume MoveFileEx with WRITE_THROUGH are the available boundary.
func syncDirectory(string) error { return nil }

func replaceFile(from, to string) error {
	source, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(source, target, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
