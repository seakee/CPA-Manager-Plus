//go:build windows

package update

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func replaceRegularFile(source, destination string) error {
	if _, err := os.Lstat(destination); os.IsNotExist(err) {
		return os.Rename(source, destination)
	}
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	var lastErr error
	deadline := time.Now().Add(10 * time.Second)
	for {
		lastErr = windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		if lastErr == nil {
			return nil
		}
		if !errors.Is(lastErr, windows.ERROR_ACCESS_DENIED) && !errors.Is(lastErr, windows.ERROR_SHARING_VIOLATION) {
			return lastErr
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		time.Sleep(100 * time.Millisecond)
	}
}
