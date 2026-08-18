package update

import (
	"os"
	"os/exec"
)

func StartDetachedUpdater(binary, transactionPath string) (*os.Process, error) {
	command := exec.Command(binary, "apply", "--transaction", transactionPath)
	configureDetachedProcess(command)
	if err := command.Start(); err != nil {
		return nil, err
	}
	return command.Process, nil
}
