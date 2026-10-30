package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// diskCheck compares the copy requirement (source bytes plus 20% headroom) with
// the free space of the volume that will receive the copy, and states the
// scratch requirement beside it: the snapshot copy needs room for the same
// bytes on the scratch volume before the target volume is used at all. A probe
// that fails is a blocking condition: an unmeasured volume is never assumed to
// be big enough.
//
// The measured directory is never the working directory. An empty target falls
// back to the configured scratch directory, and then to the OS temporary
// directory that would hold the scratch copy; neither is the process cwd, which
// says nothing about either volume.
func diskCheck(sourceBytes int64, targetDir string, options Options) (Check, bool) {
	required := sourceBytes + (sourceBytes+4)/5
	// The private snapshot copy is the same size as the source. It is written
	// before anything reaches the target volume, so both volumes need the room.
	scratchRequired := sourceBytes
	probe := options.AvailableBytes
	if probe == nil {
		probe = availableBytes
	}
	target, defaulted := diskTarget(targetDir, options.ScratchDir)
	available, err := probe(nearestExistingDir(target))
	volume := ""
	if defaulted {
		volume = "; targetVolume=scratch (no --target-dir given)"
	}
	if err != nil {
		return Check{
			Name: CheckDisk, Status: StatusBlocked, Code: CodeDiskProbeFailed,
			Detail: fmt.Sprintf("requiredBytes=%d scratchRequiredBytes=%d availableBytes=unavailable: %s%s",
				required, scratchRequired, rootCause(err), volume),
		}, true
	}
	detail := fmt.Sprintf("requiredBytes=%d availableBytes=%d; sourceBytes=%d scratchRequiredBytes=%d%s",
		required, available, sourceBytes, scratchRequired, volume)
	if available < required {
		return Check{Name: CheckDisk, Status: StatusBlocked, Code: CodeDiskInsufficient, Detail: detail}, true
	}
	return Check{Name: CheckDisk, Status: StatusPass, Detail: detail}, false
}

// diskTarget resolves which directory the probe measures. It never returns the
// working directory: an unset target is the scratch volume, and an unset
// scratch volume is the OS temporary directory the snapshot would use.
func diskTarget(targetDir string, scratchDir string) (string, bool) {
	if trimmed := strings.TrimSpace(targetDir); trimmed != "" {
		return trimmed, false
	}
	if trimmed := strings.TrimSpace(scratchDir); trimmed != "" {
		return trimmed, true
	}
	return os.TempDir(), true
}

// nearestExistingDir resolves the directory whose volume the probe should
// measure. A volume that has not been created yet still belongs to an existing
// parent volume, and probing a path that does not exist would report a false
// failure.
func nearestExistingDir(dir string) string {
	current := dir
	if current == "" {
		current = os.TempDir()
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
