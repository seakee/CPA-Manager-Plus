package upgrade

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
)

var (
	platformAvailableBytes = availableBytes
	filesystemIdentity     = copyinmarker.Identity
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
func diskCheck(sourceBytes int64, targetDir string, options Options, snapshot *Snapshot) (Check, bool) {
	// The private snapshot copy is the same size as the source. It is written
	// before anything reaches the target volume, so both volumes need the room.
	scratchRequired := sourceBytes
	probe := options.AvailableBytes
	if probe == nil {
		probe = func(dir string) (int64, error) {
			available, err := platformAvailableBytes(dir)
			if err != nil {
				return 0, err
			}
			return addReclaimableSnapshotBytes(available, dir, snapshot), nil
		}
	}
	trimmedTarget := strings.TrimSpace(targetDir)
	targetReady := false
	if options.ReconcileTargetReady && trimmedTarget != "" {
		if info, err := os.Lstat(TargetDatabasePath(trimmedTarget)); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			targetReady = true
		}
	}
	if targetReady {
		required := (sourceBytes + 4) / 5
		target, _ := diskTarget(trimmedTarget, options.ScratchDir)
		available, err := probe(nearestExistingDir(target))
		if err != nil {
			return Check{
				Name: CheckDisk, Status: StatusBlocked, Code: CodeDiskProbeFailed,
				Detail: fmt.Sprintf("requiredBytes=%d scratchRequiredBytes=%d availableBytes=unavailable: %s",
					required, scratchRequired, rootCause(err)),
			}, true
		}
		scratchTarget, _ := diskTarget("", options.ScratchDir)
		scratchAvailable, scratchErr := probe(nearestExistingDir(scratchTarget))
		if scratchErr != nil {
			return Check{
				Name: CheckDisk, Status: StatusBlocked, Code: CodeDiskProbeFailed,
				Detail: fmt.Sprintf("requiredBytes=%d availableBytes=%d; sourceBytes=%d scratchRequiredBytes=%d scratchAvailableBytes=unavailable: %s",
					required, available, sourceBytes, scratchRequired, rootCause(scratchErr)),
			}, true
		}
		detail := fmt.Sprintf("requiredBytes=%d availableBytes=%d; sourceBytes=%d scratchRequiredBytes=%d scratchAvailableBytes=%d",
			required, available, sourceBytes, scratchRequired, scratchAvailable)
		if available < required || scratchAvailable < scratchRequired {
			return Check{Name: CheckDisk, Status: StatusBlocked, Code: CodeDiskInsufficient, Detail: detail}, true
		}
		return Check{Name: CheckDisk, Status: StatusPass, Detail: detail}, false
	}
	required := sourceBytes + (sourceBytes+4)/5
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

// addReclaimableSnapshotBytes credits back the bytes Preflight's own temporary
// snapshot currently occupies when the probed directory resides on the same
// filesystem volume as that temporary snapshot directory. Preflight removes the
// snapshot before returning, so real OS free-space probes must not double-count
// it against the copy or scratch requirement.
func addReclaimableSnapshotBytes(available int64, probedDir string, snapshot *Snapshot) int64 {
	if available < 0 || snapshot == nil {
		return available
	}
	reclaimable := snapshot.Bytes()
	if reclaimable <= 0 {
		return available
	}
	if !sameFilesystemVolume(probedDir, snapshot.Dir()) {
		return available
	}
	if available > math.MaxInt64-reclaimable {
		return math.MaxInt64
	}
	return available + reclaimable
}

func sameFilesystemVolume(probedDir string, snapshotDir string) bool {
	if probedDir == "" || snapshotDir == "" {
		return false
	}
	probedID, err := filesystemIdentity(canonicalPath(probedDir))
	if err != nil {
		return false
	}
	snapshotID, err := filesystemIdentity(canonicalPath(snapshotDir))
	if err != nil {
		return false
	}
	if probedID.Device == 0 || snapshotID.Device == 0 {
		return canonicalPath(probedDir) == canonicalPath(filepath.Dir(snapshotDir))
	}
	return probedID.Device == snapshotID.Device
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
		if info, err := os.Stat(current); err == nil && info.IsDir() {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return current
		}
		current = parent
	}
}
