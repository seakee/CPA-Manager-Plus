// Package legacypreflight: the rollback half of the Upgrade admission seam.
//
// A rolled-back operation is not a completed one. The v2 root was abandoned and
// the 1.x stack was put back in charge, so the process that starts against that
// root must stay on the Upgrade surface: serving the console, the key workspace
// or gateway traffic would present an abandoned install as a working one. The
// stage mapping in admission.go is the blocking half; the reason below is what
// the console can show, so the operator learns why the normal surfaces are
// closed instead of reading a generic "in progress".
package legacypreflight

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

// UpgradeReason is the typed, operator-facing explanation attached to an
// admission that restricts the surface. An empty reason means the restriction,
// if any, has no more specific explanation.
type UpgradeReason string

const (
	// ReasonUpgradeRolledBack reports that the recorded operation was rolled
	// back to 1.x. It is not an error and not a completion: the v2 root stays
	// closed until an operator resolves it.
	ReasonUpgradeRolledBack UpgradeReason = "upgrade_rolled_back"
)

// UpgradeReasonAdmission is the optional extension of UpgradeAdmission: a reader
// that can also explain its restriction. It is optional so an existing reader
// keeps compiling and keeps its exact behavior; a reader that omits it simply
// produces no reason.
type UpgradeReasonAdmission interface {
	UpgradeAdmission
	AdmittedReason(ctx context.Context) (UpgradeStage, UpgradeReason, error)
}

// AdmittedReason reports the admission stage and, when it restricts the
// surface, the typed reason. A rolled-back row is read as in-progress plus
// ReasonUpgradeRolledBack, so it never reaches the normal surfaces.
func (a DatabaseUpgradeAdmission) AdmittedReason(ctx context.Context) (UpgradeStage, UpgradeReason, error) {
	path := strings.TrimSpace(a.DatabasePath)
	if path == "" {
		return UpgradeStageNone, "", errors.New("upgrade admission requires the Manager database path")
	}
	// Do not open an unpublished target or any foreign database sidecars.
	marker, inFlight, err := readCopyInMarker(path)
	if err != nil {
		return UpgradeStageNone, "", err
	}
	if inFlight && marker.AwaitingCommit(path) {
		return UpgradeStageInProgress, "", nil
	}
	record, found, err := readRecordedUpgrade(ctx, a)
	if err != nil {
		return UpgradeStageNone, "", err
	}
	if found {
		stage, err := upgradeStageFromRecord(record)
		if err != nil {
			return UpgradeStageNone, "", err
		}
		return stage, upgradeReasonFromRecord(record), nil
	}
	// No row: an unfinished copy-in may still be described by its marker, and a
	// marker never carries a rollback. The marker is read again because the
	// record read above may have raced a first copy-in writing it.
	if _, inFlight, err := readCopyInMarker(path); err != nil {
		return UpgradeStageNone, "", err
	} else if inFlight {
		return UpgradeStageInProgress, "", nil
	}
	return UpgradeStageNone, "", nil
}

// upgradeReasonFromRecord is the reason half of the same decision
// upgradeStageFromRecord makes. It is derived from the durable stage alone.
func upgradeReasonFromRecord(record upgradeRecord) UpgradeReason {
	return ReasonForStage(record.Stage)
}

// ReasonForStage reports the console reason an admission attaches to a durable
// stage. Startup admission and `upgrade status` both call it, so the two can
// never describe the same row differently.
func ReasonForStage(stage model.UpgradeStage) UpgradeReason {
	if stage == model.UpgradeStageRolledBack {
		return ReasonUpgradeRolledBack
	}
	return ""
}

// upgradeAdmissionStageAndReason resolves the injected reader once per
// admission, preferring the reason-aware form. Both halves come from the same
// read, so a stage and a reason can never describe different records.
func upgradeAdmissionStageAndReason(ctx context.Context, reader UpgradeAdmission) (UpgradeStage, UpgradeReason, error) {
	if reader == nil {
		return UpgradeStageNone, "", nil
	}
	if reasoned, ok := reader.(UpgradeReasonAdmission); ok {
		stage, reason, err := reasoned.AdmittedReason(ctx)
		if err != nil {
			return UpgradeStageNone, "", fmt.Errorf("read the Upgrade operation: %w", err)
		}
		if !stage.valid() {
			return UpgradeStageNone, "", fmt.Errorf("Upgrade admission %q is not supported", string(stage))
		}
		return stage.normalized(), reason, nil
	}
	stage, err := upgradeAdmissionStage(ctx, reader)
	return stage, "", err
}
