//go:build !windows

package update

import (
	"errors"
	"os"
	"syscall"
)

func platformProcessExists(process *os.Process) (bool, error) {
	err := process.Signal(syscall.Signal(0))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, err
}
