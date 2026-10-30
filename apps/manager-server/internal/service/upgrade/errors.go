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
	// UpgradeErrorReconcileMismatch reports that the copied-in store and the 1.x
	// source disagree, or that the source can never complete a day again, and
	// the operator has not acknowledged those days. Nothing was marked and the
	// operation row did not advance.
	UpgradeErrorReconcileMismatch UpgradeErrorCode = "upgrade_reconcile_mismatch"
	// UpgradeErrorReconcileTargetIncomplete reports that the v2 store cannot show
	// a domain the 1.x source holds days of: the table is missing, or not one day
	// could be compared. That is lost data, not a disagreement, so no
	// acknowledgement can waive it. Nothing was marked and the operation row did
	// not advance.
	UpgradeErrorReconcileTargetIncomplete UpgradeErrorCode = "upgrade_reconcile_target_incomplete"
	// UpgradeErrorReconcileNothingCompared reports that the run established no
	// operation domain at all: the 1.x source holds none of the compared tables,
	// so nothing was compared. A run without a comparison has no result to
	// record, so the operation row did not advance and nothing was marked
	// instead of a recorded stage that names no domain.
	UpgradeErrorReconcileNothingCompared UpgradeErrorCode = "upgrade_reconcile_nothing_compared"
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
	// UpgradeErrorRollbackProbeSelf reports that the health probe addresses this
	// Manager's own listener. The 1.x and the v2 Manager state the same service
	// identity, so an answer there is the v2 stack describing itself: it proves
	// nothing about a 1.x volume. Nothing was written.
	UpgradeErrorRollbackProbeSelf UpgradeErrorCode = "upgrade_rollback_probe_self"
	// UpgradeErrorRollbackPortCollision reports that the plan would publish the
	// port this Manager itself listens on. The v2 stack holds that port while it
	// runs, so a v1 started there could never prove it is the one answering.
	// Nothing was written.
	UpgradeErrorRollbackPortCollision UpgradeErrorCode = "upgrade_rollback_port_collision"
	// UpgradeErrorRollbackPortBusy reports that something already holds the port
	// the plan would publish. A v1 cannot be started there, and an operator who
	// applied the plan anyway would probe whatever holds it. Nothing was written.
	UpgradeErrorRollbackPortBusy UpgradeErrorCode = "upgrade_rollback_port_busy"
	// UpgradeErrorRollbackV2Running reports that the v2 stack still answers on
	// the address the operator named, so it was not stopped before the plan was
	// derived. Nothing was written.
	UpgradeErrorRollbackV2Running UpgradeErrorCode = "upgrade_rollback_v2_running"
	// UpgradeErrorStoreSchemaOutdated reports that the Manager store predates the
	// current build's upgrade table, so its operation row cannot be read without
	// the columns a writable open adds. The store must be opened once by this
	// build; nothing was written.
	UpgradeErrorStoreSchemaOutdated UpgradeErrorCode = "upgrade_store_schema_outdated"
	// UpgradeErrorCPASourceMissing reports that the 1.x stack holds no readable
	// managed CPA directory, configuration or auth directory. The source was not
	// read and nothing was written.
	UpgradeErrorCPASourceMissing UpgradeErrorCode = "upgrade_cpa_source_missing"
	// UpgradeErrorCPAConfigInvalid reports that the managed CPA configuration is
	// not a readable YAML mapping, or declares a layout this destination does not
	// load. Nothing was written.
	UpgradeErrorCPAConfigInvalid UpgradeErrorCode = "upgrade_cpa_config_invalid"
	// UpgradeErrorCPAVersionRequired reports that the source uses the v7 layout
	// and neither the operator nor the stack file names its CPA release. A v7
	// file carries no version of its own, and this import never infers one from
	// the file's shape. Nothing was written.
	UpgradeErrorCPAVersionRequired UpgradeErrorCode = "upgrade_cpa_version_required"
	// UpgradeErrorCPAVersionUnsupported reports that the source CPA release is
	// below the accepted floor, is not an exact release, or does not describe the
	// configuration it holds. Nothing was written; the operator upgrades the CPA
	// inside the 1.x stack first.
	UpgradeErrorCPAVersionUnsupported UpgradeErrorCode = "upgrade_cpa_version_unsupported"
	// UpgradeErrorCPAClientKeysMissing reports that the source CPA holds no client
	// API key (or more than a destination accepts), so the destination could not
	// be staged and verified with the keys the import would install. Nothing was
	// written.
	UpgradeErrorCPAClientKeysMissing UpgradeErrorCode = "upgrade_cpa_client_keys_missing"
	// UpgradeErrorCPAPluginChoiceRequired reports that the run carries no explicit
	// plugin-runtime decision. The choice is never inherited from the source and
	// is never defaulted. Nothing was written.
	UpgradeErrorCPAPluginChoiceRequired UpgradeErrorCode = "upgrade_cpa_plugin_choice_required"
	// UpgradeErrorCPAStageConflict reports that the recorded operation is not in a
	// stage that can enter the import, or is already past it. Nothing was
	// written.
	UpgradeErrorCPAStageConflict UpgradeErrorCode = "upgrade_cpa_stage_conflict"
	// UpgradeErrorCPADestinationUnready reports that the destination could not be
	// observed, staged or started, so no configuration was written to it.
	UpgradeErrorCPADestinationUnready UpgradeErrorCode = "upgrade_cpa_destination_unready"
	// UpgradeErrorCPAImportUnproven reports that a copied item does not read back
	// from the destination as it was written, or that an item's effect is
	// unknown. The import stops, the operation stays at cpa_import and the
	// cpa_config domain is not recorded.
	UpgradeErrorCPAImportUnproven UpgradeErrorCode = "upgrade_cpa_import_unproven"
	// UpgradeErrorCPAImportUnavailable reports that the source inventory could not
	// be read, so this run settled no item. Nothing was written to the
	// destination by this run and the cpa_config domain is not recorded.
	UpgradeErrorCPAImportUnavailable UpgradeErrorCode = "upgrade_cpa_import_unavailable"
	// UpgradeErrorCPASourceRootMismatch reports that the 1.x directory the run
	// named is not the one the recorded copy-in read, so the CPA directory beside
	// it belongs to another installation. Nothing was read from it and nothing
	// was written.
	UpgradeErrorCPASourceRootMismatch UpgradeErrorCode = "upgrade_cpa_source_root_mismatch"
	// UpgradeErrorCPADestinationKeyRequired reports that the destination CPA is
	// already running and this run holds no management secret for it: no earlier
	// run of this step recorded one and the caller supplied none. The import never
	// stages or invents a second secret for a destination it did not stage, so the
	// operator supplies the secret the Manager stored. Nothing was written.
	UpgradeErrorCPADestinationKeyRequired UpgradeErrorCode = "upgrade_cpa_destination_key_required"
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
	ErrTargetModified            = &UpgradeError{Code: UpgradeErrorTargetModified}
	ErrSourceChanged             = &UpgradeError{Code: UpgradeErrorSourceChanged}
	ErrReconcileMismatch         = &UpgradeError{Code: UpgradeErrorReconcileMismatch}
	ErrReconcileTargetIncomplete = &UpgradeError{Code: UpgradeErrorReconcileTargetIncomplete}
	ErrReconcileNothingCompared  = &UpgradeError{Code: UpgradeErrorReconcileNothingCompared}

	ErrRollbackNotApplicable = &UpgradeError{Code: UpgradeErrorRollbackNotApplicable}
	ErrRollbackUnproven      = &UpgradeError{Code: UpgradeErrorRollbackUnproven}
	ErrRollbackImageUnpinned = &UpgradeError{Code: UpgradeErrorRollbackImageUnpinned}
	ErrRollbackImageFamily   = &UpgradeError{Code: UpgradeErrorRollbackImageFamily}
	ErrRollbackVolumeUnbound = &UpgradeError{Code: UpgradeErrorRollbackVolumeUnbound}
	ErrRollbackRootsOverlap  = &UpgradeError{Code: UpgradeErrorRollbackRootsOverlap}
	ErrRollbackEvidenceStale = &UpgradeError{Code: UpgradeErrorRollbackEvidenceStale}
	ErrRollbackProbeEndpoint = &UpgradeError{Code: UpgradeErrorRollbackProbeEndpoint}
	ErrRollbackIdentity      = &UpgradeError{Code: UpgradeErrorRollbackIdentityForeign}
	ErrRollbackProbeSelf     = &UpgradeError{Code: UpgradeErrorRollbackProbeSelf}
	ErrRollbackPortCollision = &UpgradeError{Code: UpgradeErrorRollbackPortCollision}
	ErrRollbackPortBusy      = &UpgradeError{Code: UpgradeErrorRollbackPortBusy}
	ErrRollbackV2Running     = &UpgradeError{Code: UpgradeErrorRollbackV2Running}
	ErrStoreSchemaOutdated   = &UpgradeError{Code: UpgradeErrorStoreSchemaOutdated}

	ErrCPASourceMissing      = &UpgradeError{Code: UpgradeErrorCPASourceMissing}
	ErrCPAConfigInvalid      = &UpgradeError{Code: UpgradeErrorCPAConfigInvalid}
	ErrCPAVersionRequired    = &UpgradeError{Code: UpgradeErrorCPAVersionRequired}
	ErrCPAVersionUnsupported = &UpgradeError{Code: UpgradeErrorCPAVersionUnsupported}
	ErrCPAClientKeysMissing  = &UpgradeError{Code: UpgradeErrorCPAClientKeysMissing}
	ErrCPAPluginChoice       = &UpgradeError{Code: UpgradeErrorCPAPluginChoiceRequired}
	ErrCPAStageConflict      = &UpgradeError{Code: UpgradeErrorCPAStageConflict}
	ErrCPADestinationUnready = &UpgradeError{Code: UpgradeErrorCPADestinationUnready}
	ErrCPAImportUnproven     = &UpgradeError{Code: UpgradeErrorCPAImportUnproven}
	ErrCPAImportUnavailable  = &UpgradeError{Code: UpgradeErrorCPAImportUnavailable}
	ErrCPASourceRootMismatch = &UpgradeError{Code: UpgradeErrorCPASourceRootMismatch}
	ErrCPADestinationKey     = &UpgradeError{Code: UpgradeErrorCPADestinationKeyRequired}
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
