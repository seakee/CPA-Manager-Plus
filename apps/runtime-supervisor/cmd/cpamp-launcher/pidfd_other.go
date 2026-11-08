//go:build unix && !linux

package main

import "syscall"

func openOSPIDHandle(_ int) (PIDHandle, error) {
	return nil, syscall.ENOSYS
}
