package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
)

// diskCheck compares the copy requirement (source bytes plus 20% headroom) with
// the free space of the volume that will receive the copy. A probe that fails is
// a blocking condition: an unmeasured volume is never assumed to be big enough.
func diskCheck(sourceBytes int64, targetDir string, options Options) (Check, bool) {
	required := sourceBytes + (sourceBytes+4)/5
	probe := options.AvailableBytes
	if probe == nil {
		probe = availableBytes
	}
	target := nearestExistingDir(targetDir)
	available, err := probe(target)
	if err != nil {
		return Check{
			Name: CheckDisk, Status: StatusBlocked, Code: CodeDiskProbeFailed,
			Detail: fmt.Sprintf("requiredBytes=%d availableBytes=unavailable: %s", required, rootCause(err)),
		}, true
	}
	detail := fmt.Sprintf("requiredBytes=%d availableBytes=%d; sourceBytes=%d", required, available, sourceBytes)
	if available < required {
		return Check{Name: CheckDisk, Status: StatusBlocked, Code: CodeDiskInsufficient, Detail: detail}, true
	}
	return Check{Name: CheckDisk, Status: StatusPass, Detail: detail}, false
}

// nearestExistingDir resolves the directory whose volume the probe should
// measure. A volume that has not been created yet still belongs to an existing
// parent volume, and probing a path that does not exist would report a false
// failure.
func nearestExistingDir(dir string) string {
	current := dir
	if current == "" {
		current = "."
	}
	if absolute, err := filepath.Abs(current); err == nil {
		current = absolute
	}
	for {
		if _, err := os.Stat(current); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return current
		}
		current = parent
	}
}
