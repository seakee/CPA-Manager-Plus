//go:build !windows

package copyinmarker

import (
	"errors"
	"os"
	"syscall"
)

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
