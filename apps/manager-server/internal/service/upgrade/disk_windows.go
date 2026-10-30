//go:build windows

package upgrade

import "golang.org/x/sys/windows"

// availableBytes reports the free space available to this process on the volume
// holding path.
func availableBytes(path string) (int64, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var freeToCaller, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(pointer, &freeToCaller, &total, &totalFree); err != nil {
		return 0, err
	}
	return int64(freeToCaller), nil
}
