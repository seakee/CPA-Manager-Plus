//go:build unix && !linux

package main

import "syscall"

func openOSPIDHandle(_ int) (PIDHandle, error) {
	return nil, syscall.ENOSYS
}

func statfsMagic(_ string) (int64, error) {
	return 0, syscall.ENOSYS
}

func lookupMountID(_ string) (int, error) {
	return 0, syscall.ENOSYS
}
