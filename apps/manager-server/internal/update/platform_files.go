package update

import (
	"runtime"
)

type ManagedFiles struct {
	Binary  string
	Updater string
	Control string
}

func RuntimeManagedFiles() ManagedFiles {
	if runtime.GOOS == "windows" {
		return ManagedFiles{
			Binary:  "cpa-manager-plus.exe",
			Updater: "cpa-manager-plus-updater.exe",
			Control: "cpa-manager-plusctl.ps1",
		}
	}
	return ManagedFiles{
		Binary:  "cpa-manager-plus",
		Updater: "cpa-manager-plus-updater",
		Control: "cpa-manager-plusctl",
	}
}
