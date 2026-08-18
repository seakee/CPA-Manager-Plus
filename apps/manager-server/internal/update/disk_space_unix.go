//go:build !windows

package update

import "golang.org/x/sys/unix"

func diskFreeBytes(path string) (uint64, error) {
	if err := ensurePrivateDirectory(path); err != nil {
		return 0, err
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
