//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

type linuxPIDFDHandle struct {
	fd int
}

func openOSPIDHandle(pid int) (PIDHandle, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, err
	}
	return linuxPIDFDHandle{fd: fd}, nil
}

func (h linuxPIDFDHandle) Signal(sig syscall.Signal) error {
	return unix.PidfdSendSignal(h.fd, unix.Signal(sig), nil, 0)
}

func (h linuxPIDFDHandle) Close() error {
	return unix.Close(h.fd)
}

func statfsMagic(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Type), nil
}

func lookupMountID(procPath string) (int, error) {
	fd, err := unix.Openat(unix.AT_FDCWD, procPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	defer unix.Close(fd)

	fdInfoPath := filepath.Join(procPath, "self", "fdinfo", strconv.Itoa(fd))
	raw, err := os.ReadFile(fdInfoPath)
	if err != nil {
		return 0, err
	}
	return parseFDInfoMountID(raw)
}
