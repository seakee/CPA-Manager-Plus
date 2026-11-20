//go:build linux

package main

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func openGatewayEntryNoFollow(dirFD int, name, childPath string) (int, unix.Stat_t, error) {
	fd, err := unix.Openat(dirFD, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, unix.Stat_t{}, fmt.Errorf("failed to open gateway entry %s: %w", childPath, err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return -1, unix.Stat_t{}, fmt.Errorf("failed to fstat %s: %w", childPath, err)
	}
	return fd, st, nil
}

func fchownGatewayEntryFD(fd int, uid, gid int) error {
	return unix.Fchownat(fd, "", uid, gid, unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW)
}

func openGatewayDirFromEntryFD(entryFD int, childPath string) (int, error) {
	dirFD, err := unix.Openat(entryFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("Gateway state path is not a real directory: %s: %w", childPath, err)
	}
	return dirFD, nil
}
