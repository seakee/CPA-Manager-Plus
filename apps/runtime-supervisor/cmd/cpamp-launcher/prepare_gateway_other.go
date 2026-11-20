//go:build unix && !linux

package main

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func openGatewayEntryNoFollow(dirFD int, name, childPath string) (int, unix.Stat_t, error) {
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EMLINK) {
			return -1, unix.Stat_t{}, fmt.Errorf("Gateway state path has unsupported file type (symbolic link): %s: %w", childPath, err)
		}
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
	return unix.Fchown(fd, uid, gid)
}

func openGatewayDirFromEntryFD(entryFD int, childPath string) (int, error) {
	dupFD, err := unix.Dup(entryFD)
	if err != nil {
		return -1, fmt.Errorf("Gateway state path is not a real directory: %s: %w", childPath, err)
	}
	unix.CloseOnExec(dupFD)
	return dupFD, nil
}
