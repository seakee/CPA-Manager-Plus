package upgrade

import (
	"errors"
	"fmt"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
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
	// UpgradeErrorTargetConflict reports that the v2 data root cannot be resumed:
	// a copy-in marker describes it, but the marker and the members beside it
	// disagree, or the members no longer match the backup the marker names.
	// Nothing was deleted or overwritten; the marker is the evidence that the
	// store is unfinished, so it stays exactly where it is.
	UpgradeErrorTargetConflict UpgradeErrorCode = "upgrade_target_conflict"
	// UpgradeErrorV2Running reports that another Manager process owns the v2
	// data root: its process lock is held, so the copy-in refuses before it
	// creates, backs up, restores, migrates or records anything. Stop the
	// process and rerun; nothing this run intended was written.
	UpgradeErrorV2Running UpgradeErrorCode = "upgrade_copy_in_v2_running"
	// UpgradeErrorTargetLocked reports that another process holds the v2
	// data-root lock, so this run cannot write the store. Nothing was compared
	// and nothing was marked.
	UpgradeErrorTargetLocked UpgradeErrorCode = "upgrade_target_locked"
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
	// UpgradeErrorReconcileNotCopiedIn reports that the v2 data root holds no
	// recorded copy-in, so there is nothing to reconcile against. Nothing was
	// compared and nothing was marked.
	UpgradeErrorReconcileNotCopiedIn UpgradeErrorCode = "upgrade_reconcile_not_copied_in"
	// UpgradeErrorReconcileStageClosed reports that the recorded operation
	// already moved past the reconcile step, or a rollback closed it, so this
	// step could never record its result. Nothing was compared and nothing was
	// marked.
	UpgradeErrorReconcileStageClosed UpgradeErrorCode = "upgrade_reconcile_stage_closed"
	// UpgradeErrorReconcileRecordIncomplete reports that the recorded copy-in
	// does not name the source fingerprint it copied, so the 1.x volume cannot be
	// proven to be the one that was copied. Nothing was compared and nothing was
	// marked.
	UpgradeErrorReconcileRecordIncomplete UpgradeErrorCode = "upgrade_reconcile_record_incomplete"
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
	// UpgradeErrorSnapshotFailed reports that a private snapshot could not be
	// created, or would have to be created inside a root this operation must not
	// write to. The snapshot copies the whole database, so a scratch directory
	// under the 1.x source or the Manager data root is refused before a directory
	// is made; nothing was written.
	UpgradeErrorSnapshotFailed UpgradeErrorCode = "upgrade_snapshot_failed"
	// UpgradeErrorStoreSchemaOutdated reports that the Manager store predates the
	// current build's upgrade table, so its operation row cannot be read without
	// the columns a writable open adds. It also reports the related row a writable
	// open cannot repair: a rolled_back stage whose evidence did not survive, which
	// only a build that recorded the two separately could write. Both commands that
	// read the row report it with this code and exit 1; nothing was written.
	UpgradeErrorStoreSchemaOutdated UpgradeErrorCode = "upgrade_store_schema_outdated"

	// --- External Connect (U1-5) ---
	//
	// This group is the External connect step's own vocabulary. Every value is
	// the same string the Fresh External flow and the external/rebind route
	// already use, so an upgrade refusal and a live External refusal say the
	// same words about the same CPA.
	//
	// UpgradeErrorExternalVersionUnsupported reports a user's CPA that did not
	// advertise the v8 contract this Manager writes (a v7 CPA, or one whose
	// version could not be read). The step stops: the operation row keeps its
	// stage, carries this code, and the owner upgrades the CPA first. There is
	// no continue-anyway path.
	UpgradeErrorExternalVersionUnsupported UpgradeErrorCode = cpa.ReasonExternalVersionUnsupported
	// UpgradeErrorExternalConfigDocumentInvalid reports that the CPA at the
	// address answered, but what it served on the v8 read cannot be a v8
	// configuration document: not JSON, truncated, a port outside the TCP range,
	// or a client-key list that is not a list of strings. It is deliberately
	// apart from UpgradeErrorExternalVersionUnsupported, because that one tells
	// the operator to upgrade a CPA that is already v8: here the CPA may be v8
	// and the document, or a proxy rewriting it, is what failed. Nothing was
	// connected and the operation did not advance.
	UpgradeErrorExternalConfigDocumentInvalid UpgradeErrorCode = "external_cpa_config_document_invalid"
	// UpgradeErrorExternalManagementRejected reports the user's CPA refusing the
	// management credential — a 401, or the 403 it answers when it bans this
	// client — so the key is the problem, never the version. Only rebinding the
	// connection resolves it.
	UpgradeErrorExternalManagementRejected UpgradeErrorCode = freshconfig.ReasonManagementRejected
	// UpgradeErrorExternalTargetMoved reports that the recorded External target
	// no longer names the connection this operation connected: the address
	// changed after it was committed. The identity and generation recorded by
	// the successful connect are the comparison, so a moved target is refused
	// instead of silently reconnected.
	UpgradeErrorExternalTargetMoved UpgradeErrorCode = freshconfig.ReasonTargetMoved
	// UpgradeErrorExternalAuthorityUnavailable reports a target that could not
	// be reached, or one that answered without proving the surface the step
	// needs (the model surface, or the client-key list). It is an environment
	// or target condition, not a decision about the migration.
	UpgradeErrorExternalAuthorityUnavailable UpgradeErrorCode = "external_cpa_authority_unavailable"
	// UpgradeErrorExternalNotApplicable reports that the recorded operation is
	// not at a position the External connect step can run from, or that the v2
	// data root holds no recorded copy-in. Nothing was written.
	UpgradeErrorExternalNotApplicable UpgradeErrorCode = "upgrade_external_connect_not_applicable"
	// UpgradeErrorExternalV2Running reports that another Manager process owns the
	// v2 data root: the step holds that process's ownership fence across its whole
	// write path, and it refuses before it migrates, probes or records anything
	// rather than writing a store two processes would then disagree about. Stop
	// the process and run the step again.
	UpgradeErrorExternalV2Running UpgradeErrorCode = "upgrade_external_connect_v2_running"

	// UpgradeErrorStoreLocked reports that the Manager store's process lock is
	// held, so an offline migration cannot run. The lock is the proof that the
	// writable Manager is still serving the store.
	UpgradeErrorStoreLocked UpgradeErrorCode = "upgrade_store_locked"
	// UpgradeErrorMigrateNotCopiedIn reports that `upgrade migrate` was asked to
	// open a store that carries no recorded Upgrade copy-in. A 1.x store or a
	// native v2 store without that row would be migrated into a store this build
	// admits as native, without the copy evidence the Upgrade admission
	// requires, so the command refuses before any writable open and nothing is
	// written.
	UpgradeErrorMigrateNotCopiedIn UpgradeErrorCode = "upgrade_migrate_not_copied_in"
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
	ErrTargetConflict            = &UpgradeError{Code: UpgradeErrorTargetConflict}
	ErrV2Running                 = &UpgradeError{Code: UpgradeErrorV2Running}
	ErrTargetLocked              = &UpgradeError{Code: UpgradeErrorTargetLocked}
	ErrReconcileMismatch         = &UpgradeError{Code: UpgradeErrorReconcileMismatch}
	ErrReconcileTargetIncomplete = &UpgradeError{Code: UpgradeErrorReconcileTargetIncomplete}
	ErrReconcileNotCopiedIn      = &UpgradeError{Code: UpgradeErrorReconcileNotCopiedIn}
	ErrReconcileStageClosed      = &UpgradeError{Code: UpgradeErrorReconcileStageClosed}
	ErrReconcileRecordIncomplete = &UpgradeError{Code: UpgradeErrorReconcileRecordIncomplete}
	ErrReconcileNothingCompared  = &UpgradeError{Code: UpgradeErrorReconcileNothingCompared}
	ErrRollbackNotApplicable     = &UpgradeError{Code: UpgradeErrorRollbackNotApplicable}
	ErrRollbackUnproven          = &UpgradeError{Code: UpgradeErrorRollbackUnproven}
	ErrRollbackImageUnpinned     = &UpgradeError{Code: UpgradeErrorRollbackImageUnpinned}
	ErrRollbackImageFamily       = &UpgradeError{Code: UpgradeErrorRollbackImageFamily}
	ErrRollbackVolumeUnbound     = &UpgradeError{Code: UpgradeErrorRollbackVolumeUnbound}
	ErrRollbackRootsOverlap      = &UpgradeError{Code: UpgradeErrorRollbackRootsOverlap}
	ErrRollbackEvidenceStale     = &UpgradeError{Code: UpgradeErrorRollbackEvidenceStale}
	ErrRollbackProbeEndpoint     = &UpgradeError{Code: UpgradeErrorRollbackProbeEndpoint}
	ErrRollbackIdentity          = &UpgradeError{Code: UpgradeErrorRollbackIdentityForeign}
	ErrRollbackProbeSelf         = &UpgradeError{Code: UpgradeErrorRollbackProbeSelf}
	ErrRollbackPortCollision     = &UpgradeError{Code: UpgradeErrorRollbackPortCollision}
	ErrRollbackPortBusy          = &UpgradeError{Code: UpgradeErrorRollbackPortBusy}
	ErrRollbackV2Running         = &UpgradeError{Code: UpgradeErrorRollbackV2Running}
	ErrSnapshotScratch           = &UpgradeError{Code: UpgradeErrorSnapshotFailed}
	ErrStoreSchemaOutdated       = &UpgradeError{Code: UpgradeErrorStoreSchemaOutdated}

	// External Connect (U1-5). A v7 CPA, a rejected management credential and a
	// moved target are decisions the owner has to look at; an unreachable target
	// is an environment condition.
	ErrExternalVersionUnsupported    = &UpgradeError{Code: UpgradeErrorExternalVersionUnsupported}
	ErrExternalConfigDocumentInvalid = &UpgradeError{Code: UpgradeErrorExternalConfigDocumentInvalid}
	ErrExternalManagementRejected    = &UpgradeError{Code: UpgradeErrorExternalManagementRejected}
	ErrExternalTargetMoved           = &UpgradeError{Code: UpgradeErrorExternalTargetMoved}
	ErrExternalAuthorityUnavailable  = &UpgradeError{Code: UpgradeErrorExternalAuthorityUnavailable}
	ErrExternalNotApplicable         = &UpgradeError{Code: UpgradeErrorExternalNotApplicable}
	// ErrExternalV2Running is a refusal, not a transient failure: the operator
	// stops the process that owns the store and reruns the step.
	ErrExternalV2Running = &UpgradeError{Code: UpgradeErrorExternalV2Running}

	// StoreLocked and MigrateNotCopiedIn belong to the offline migration the
	// rollback acceptance needs: the first reports the live Manager still
	// holding the store, the second a store the command must not make native
	// without the copy-in evidence.
	ErrStoreLocked        = &UpgradeError{Code: UpgradeErrorStoreLocked}
	ErrMigrateNotCopiedIn = &UpgradeError{Code: UpgradeErrorMigrateNotCopiedIn}
)

// The CPA import step's own vocabulary (U1-4). Each code is the one the import
// command and its executor return, so the CLI exit code and the wizard copy read
// the same word for the same refusal.
const (
	// UpgradeErrorCPASourceMissing reports that the 1.x stack holds no readable
	// managed CPA directory, configuration or auth directory. The source was not
	// read and nothing was written.
	UpgradeErrorCPASourceMissing UpgradeErrorCode = "upgrade_cpa_source_missing"

	// UpgradeErrorCPAConfigInvalid reports that the managed CPA configuration is
	// not a readable YAML mapping, or declares a layout this destination does not
	// load. Nothing was written.
	UpgradeErrorCPAConfigInvalid UpgradeErrorCode = "upgrade_cpa_config_invalid"

	// UpgradeErrorCPAVersionRequired reports that the source uses the v7 layout
	// and neither the operator nor the stack's compose/environment files name an
	// exact CPA release. A v7 file carries no version of its own, and this import
	// never infers one from the file's shape. Nothing was written.
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
	// stage the import may be entered from — it runs after reconciliation and
	// resumes from its own unrecorded stage — or is already past it. This run
	// read and wrote nothing; the recorded position did not move.
	UpgradeErrorCPAStageConflict UpgradeErrorCode = "upgrade_cpa_stage_conflict"

	// UpgradeErrorCPADestinationUnready reports that the destination could not be
	// observed, staged or started, so no configuration was written to it.
	UpgradeErrorCPADestinationUnready UpgradeErrorCode = "upgrade_cpa_destination_unready"

	// UpgradeErrorCPAImportUnproven reports that the inventory is not the clean
	// deferral this build records: the executor copied an item without a migration
	// decision, or left an item's outcome unknown or the inventory partial. The
	// import stops, the operation stays at cpa_import and the cpa_config domain is
	// not recorded.
	UpgradeErrorCPAImportUnproven UpgradeErrorCode = "upgrade_cpa_import_unproven"

	// UpgradeErrorCPAImportUnavailable reports that the source inventory could not
	// be read, so this run settled no item. Nothing was written to the
	// destination by this run and the cpa_config domain is not recorded.
	UpgradeErrorCPAImportUnavailable UpgradeErrorCode = "upgrade_cpa_import_unavailable"

	// UpgradeErrorCPASourceLocked reports that the 1.x source ownership marker
	// this step holds for its whole read/write path could not be taken: another
	// process holds it, or the source carries no marker at all. The source is
	// then not provably stopped, so nothing was read from it and nothing was
	// written.
	UpgradeErrorCPASourceLocked UpgradeErrorCode = "upgrade_cpa_source_locked"

	// UpgradeErrorCPASourceRootMismatch reports that the 1.x directory the run
	// named is not the one the recorded copy-in read, so the CPA directory beside
	// it belongs to another installation. Nothing was read from it and nothing
	// was written.
	UpgradeErrorCPASourceRootMismatch UpgradeErrorCode = "upgrade_cpa_source_root_mismatch"

	// UpgradeErrorCPADestinationRequestChanged reports that an earlier run of this
	// step already submitted a different request to this destination — another
	// secret, plugin choice or client key set, a changed source identity or class,
	// a Runtime identity, generation or active artifact the recorded authority no
	// longer matches, or a destination URL that an earlier run proved to be the
	// staged destination — and the Supervisor replays the journalled attempt as it
	// was submitted. The run refuses instead of overwriting the record, and it
	// wrote nothing to the destination.
	UpgradeErrorCPADestinationRequestChanged UpgradeErrorCode = "upgrade_cpa_destination_request_changed"

	// UpgradeErrorCPAImportTargetForeign reports that the CPA answering at the
	// destination URL does not hold the client keys this import stages, so it is
	// not the target this operation staged. The import refuses before writing any
	// configuration or credential to it.
	UpgradeErrorCPAImportTargetForeign UpgradeErrorCode = "upgrade_cpa_import_target_foreign"

	// UpgradeErrorCPAImportTargetUnreachable reports that nothing answered at the
	// destination URL: the connection was refused, the name did not resolve, or no
	// answer arrived within the probe budget. That is not a statement about whether
	// the URL is right — a destination that is not listening yet is unreachable
	// too, and so is the stopped 1.x CPA the CLI's default CPA_UPSTREAM_URL usually
	// names — and the operator is not told to start that CPA. A started 1.x CPA
	// rejects an absent and an unknown credential, so it passes the
	// unauthenticated probe and is then sent the management secret this run staged;
	// the recovery is to name the destination instead (--destination-url) once it
	// listens. While no run has proved a URL the attempt holds none, so a retry with
	// the same or a different URL is still accepted; once a run proved one, the
	// attempt stays bound to it and a different URL is refused as
	// upgrade_cpa_destination_request_changed. Nothing was written either way.
	UpgradeErrorCPAImportTargetUnreachable UpgradeErrorCode = "upgrade_cpa_import_target_unreachable"

	// UpgradeErrorCPAConnectionConflict reports that the Manager already holds a
	// complete CPA connection to a different CPA, so handing it this destination
	// would repoint the Console away from a CPA it manages. The import refuses and
	// leaves the destination attempt recorded, so a later run completes once the
	// stored connection is rebound explicitly.
	UpgradeErrorCPAConnectionConflict UpgradeErrorCode = "upgrade_cpa_connection_conflict"

	// UpgradeErrorCPADestinationKeyRequired reports that the destination CPA is
	// already running and this run holds no management secret for it: no earlier
	// run of this step recorded one and the caller supplied none. The import never
	// stages or invents a second secret for a destination it did not stage, so the
	// operator supplies the secret the Manager stored. This run wrote nothing to
	// the destination or the operation row.
	UpgradeErrorCPADestinationKeyRequired UpgradeErrorCode = "upgrade_cpa_destination_key_required"
)

// The CPA import errors. Every one of them is a refusal that wrote nothing,
// except where its code says the destination was staged and left unproven.
var (
	ErrCPASourceMissing = &UpgradeError{Code: UpgradeErrorCPASourceMissing}

	ErrCPAConfigInvalid = &UpgradeError{Code: UpgradeErrorCPAConfigInvalid}

	ErrCPAVersionRequired = &UpgradeError{Code: UpgradeErrorCPAVersionRequired}

	ErrCPAVersionUnsupported = &UpgradeError{Code: UpgradeErrorCPAVersionUnsupported}

	ErrCPAClientKeysMissing = &UpgradeError{Code: UpgradeErrorCPAClientKeysMissing}

	ErrCPAPluginChoice = &UpgradeError{Code: UpgradeErrorCPAPluginChoiceRequired}

	ErrCPAStageConflict = &UpgradeError{Code: UpgradeErrorCPAStageConflict}

	ErrCPADestinationUnready = &UpgradeError{Code: UpgradeErrorCPADestinationUnready}

	ErrCPAImportUnproven = &UpgradeError{Code: UpgradeErrorCPAImportUnproven}

	ErrCPAImportUnavailable = &UpgradeError{Code: UpgradeErrorCPAImportUnavailable}

	ErrCPASourceLocked = &UpgradeError{Code: UpgradeErrorCPASourceLocked}

	ErrCPASourceRootMismatch = &UpgradeError{Code: UpgradeErrorCPASourceRootMismatch}

	ErrCPADestinationChanged = &UpgradeError{Code: UpgradeErrorCPADestinationRequestChanged}

	ErrCPATargetForeign = &UpgradeError{Code: UpgradeErrorCPAImportTargetForeign}

	ErrCPATargetUnreachable = &UpgradeError{Code: UpgradeErrorCPAImportTargetUnreachable}

	ErrCPAConnectionConflict = &UpgradeError{Code: UpgradeErrorCPAConnectionConflict}

	ErrCPADestinationKey = &UpgradeError{Code: UpgradeErrorCPADestinationKeyRequired}
)

func newUpgradeError(code UpgradeErrorCode, message string, err error) error {
	return &UpgradeError{Code: code, Message: message, err: err}
}

// CodedError builds a typed failure for a refusal the command layer has to
// report itself, such as a lock another process holds. The code reaches the
// CLI's JSON errorCode and its exit code through UpgradeErrorCodeOf.
func CodedError(code UpgradeErrorCode, message string, err error) error {
	return newUpgradeError(code, message, err)
}

// unresumable reports a v2 data root a copy-in may not continue from. The
// message names no host path: it is operator text, and the code is what a script
// acts on.
func unresumable(message string, err error) error {
	return newUpgradeError(UpgradeErrorTargetConflict, message, err)
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
