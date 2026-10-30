package upgrade

import (
	"errors"
	"fmt"
)

// UpgradeErrorCode is the stable, machine-readable half of an Upgrade failure.
// The human half is operator text and carries no credential and no absolute
// host path.
type UpgradeErrorCode string

const (
	// UpgradeErrorTargetModified reports that the v2 data root no longer holds
	// what the recorded copy-in materialized: a member is missing, its digest
	// changed, the archive tree gained an entry the backup does not describe, or
	// the database is not the migrated store. Nothing was overwritten.
	UpgradeErrorTargetModified UpgradeErrorCode = "upgrade_target_modified"
	// UpgradeErrorSourceChanged reports that the 1.x source changed while the
	// copy-in held its fence. The members this run moved were rolled back and
	// nothing was recorded.
	UpgradeErrorSourceChanged UpgradeErrorCode = "upgrade_source_changed"
	// UpgradeErrorRollbackNotApplicable reports that the recorded operation is
	// not in a stage a rollback can start from, or that it has already been
	// rolled back. Nothing was written.
	UpgradeErrorRollbackNotApplicable UpgradeErrorCode = "upgrade_rollback_not_applicable"
	// UpgradeErrorRollbackUnproven reports that the evidence a rollback would
	// rely on does not hold: no before/after source fingerprint, no recorded
	// backup manifest digest, or an observation that does not prove a read-only
	// open and a healthy 1.x status probe. Nothing was written.
	UpgradeErrorRollbackUnproven UpgradeErrorCode = "upgrade_rollback_unproven"
	// UpgradeErrorRollbackImageUnpinned reports that the 1.x image reference the
	// operation or the operator named carries no exact version, so a rollback
	// plan would have to fall back on a moving tag. Nothing was written.
	UpgradeErrorRollbackImageUnpinned UpgradeErrorCode = "upgrade_rollback_image_unpinned"
	// UpgradeErrorRollbackImageFamily reports that the 1.x image names a release
	// outside the recorded source's version family. A rollback restarts the
	// version the operation copied, so a different family needs the explicit
	// override. Nothing was written.
	UpgradeErrorRollbackImageFamily UpgradeErrorCode = "upgrade_rollback_image_family"
	// UpgradeErrorRollbackVolumeUnbound reports that the 1.x directory under
	// decision cannot be tied to the source root the operation recorded, so no
	// plan or verdict may claim "the original volume". Nothing was written.
	UpgradeErrorRollbackVolumeUnbound UpgradeErrorCode = "upgrade_rollback_volume_unbound"
	// UpgradeErrorRollbackRootsOverlap reports that the v2 data root and the 1.x
	// source root contain each other. The 1.x volume is a source and must never
	// become a rollback destination. Nothing was written.
	UpgradeErrorRollbackRootsOverlap UpgradeErrorCode = "upgrade_rollback_roots_overlap"
	// UpgradeErrorRollbackEvidenceStale reports that the observation a verdict
	// would rest on was taken outside the freshness window, so it no longer
	// describes the install being recorded. Nothing was written.
	UpgradeErrorRollbackEvidenceStale UpgradeErrorCode = "upgrade_rollback_evidence_stale"
	// UpgradeErrorRollbackProbeEndpoint reports that the health probe does not
	// address the 1.x `GET /health` endpoint. Every other endpoint on the 1.x
	// manager requires credentials, so a probe there proves nothing about the
	// volume. Nothing was written.
	UpgradeErrorRollbackProbeEndpoint UpgradeErrorCode = "upgrade_rollback_probe_endpoint"
	// UpgradeErrorRollbackIdentityForeign reports that something answered, but
	// not as this install: a health response naming another service, a backup
	// that is not the recorded one, or a live volume that no longer holds the
	// rows and archives the backup retained. Nothing was written.
	UpgradeErrorRollbackIdentityForeign UpgradeErrorCode = "upgrade_rollback_identity_foreign"
)

// UpgradeError is a typed Upgrade failure. Callers match it with errors.Is
// against the exported sentinels, or read Code for reporting.
type UpgradeError struct {
	Code    UpgradeErrorCode
	Message string
	err     error
}

func (e *UpgradeError) Error() string {
	if e.Message == "" {
		return string(e.Code)
	}
	if e.err == nil {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.err)
}

func (e *UpgradeError) Unwrap() error { return e.err }

// Is matches by code, so a wrapped failure still reports its kind.
func (e *UpgradeError) Is(target error) bool {
	other, ok := target.(*UpgradeError)
	return ok && other != nil && other.Code == e.Code
}

// Sentinels for errors.Is. They carry no message of their own.
var (
	ErrTargetModified        = &UpgradeError{Code: UpgradeErrorTargetModified}
	ErrSourceChanged         = &UpgradeError{Code: UpgradeErrorSourceChanged}
	ErrRollbackNotApplicable = &UpgradeError{Code: UpgradeErrorRollbackNotApplicable}
	ErrRollbackUnproven      = &UpgradeError{Code: UpgradeErrorRollbackUnproven}
	ErrRollbackImageUnpinned = &UpgradeError{Code: UpgradeErrorRollbackImageUnpinned}
	ErrRollbackImageFamily   = &UpgradeError{Code: UpgradeErrorRollbackImageFamily}
	ErrRollbackVolumeUnbound = &UpgradeError{Code: UpgradeErrorRollbackVolumeUnbound}
	ErrRollbackRootsOverlap  = &UpgradeError{Code: UpgradeErrorRollbackRootsOverlap}
	ErrRollbackEvidenceStale = &UpgradeError{Code: UpgradeErrorRollbackEvidenceStale}
	ErrRollbackProbeEndpoint = &UpgradeError{Code: UpgradeErrorRollbackProbeEndpoint}
	ErrRollbackIdentity      = &UpgradeError{Code: UpgradeErrorRollbackIdentityForeign}
)

func newUpgradeError(code UpgradeErrorCode, message string, err error) error {
	return &UpgradeError{Code: code, Message: message, err: err}
}

// UpgradeErrorCodeOf reports the code of the first typed Upgrade failure in
// err's chain.
func UpgradeErrorCodeOf(err error) (UpgradeErrorCode, bool) {
	var typed *UpgradeError
	if errors.As(err, &typed) && typed != nil {
		return typed.Code, true
	}
	return "", false
}
