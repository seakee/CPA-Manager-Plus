//go:build windows

package update

import (
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

const stillActiveExitCode = 259

func platformProcessExists(process *os.Process) (bool, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(process.Pid))
	if err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "parameter is incorrect") || strings.Contains(message, "cannot find") || strings.Contains(message, "not found") {
			return false, nil
		}
		return false, err
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	callErr := windows.GetExitCodeProcess(handle, &exitCode)
	if callErr != nil {
		return false, callErr
	}
	return exitCode == stillActiveExitCode, nil
}
