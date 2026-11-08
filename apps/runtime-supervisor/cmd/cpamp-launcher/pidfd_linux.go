//go:build linux

package main

import (
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
