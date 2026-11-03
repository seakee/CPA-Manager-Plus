//go:build !windows

package upgrade

import "golang.org/x/sys/unix"

// availableBytes reports the free space available to this process on the
// filesystem holding path.
func availableBytes(path string) (int64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	blockSize := int64(stat.Bsize)
	if blockSize <= 0 {
		return 0, unix.EINVAL
	}
	return int64(stat.Bavail) * blockSize, nil
}
