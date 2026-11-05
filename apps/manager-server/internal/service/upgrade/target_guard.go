package upgrade

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
)

// requireRecoveredCopyIn keeps every later step behind CopyIn's cleanup boundary.
// Even a readable, published committing marker must be recovered by CopyIn first.
func requireRecoveredCopyIn(databasePath string) error {
	marker, found, err := copyinmarker.Read(filepath.Dir(databasePath))
	if err != nil {
		return unresumable("the copy-in marker cannot be validated; preserve the target and verified bundle and recover with `cpa-manager-plus upgrade run` before any later upgrade step", err)
	}
	if found {
		if !copyinmarker.SameTarget(marker.TargetDatabase, databasePath) {
			return unresumable("the copy-in marker names a different database; preserve the target and marker and recover with `cpa-manager-plus upgrade run`", nil)
		}
		if marker.Phase == copyinmarker.PhaseCommitting {
			if err := copyinmarker.RequirePublishedDatabase(databasePath); err != nil {
				return unresumable("the committing copy-in target is not a sidecar-free regular database; preserve the target and marker and recover with `cpa-manager-plus upgrade run`", err)
			}
		}
		return unresumable("copy-in recovery is unfinished; preserve the target and marker and run `cpa-manager-plus upgrade run` before any later upgrade step", nil)
	}
	return requireCandidateSidecars(databasePath)
}

// A private snapshot may join regular WAL/SHM members, but it cannot prove a
// rollback journal or safely consume linked sidecars. Lstat includes empty and
// dangling entries without following them.
func requireCandidateSidecars(databasePath string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		info, err := os.Lstat(databasePath + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return unresumable("the target database sidecars cannot be inspected; preserve the target and recover the copy-in before continuing", err)
		}
		if suffix == "-journal" || !info.Mode().IsRegular() {
			return unresumable(fmt.Sprintf("the target has an unexpected %s sidecar; preserve all target members and recover the copy-in before continuing", suffix), nil)
		}
	}
	return nil
}
