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

	// External Connect (U1-5). A v7 CPA, a rejected management credential and a
	// moved target are decisions the owner has to look at; an unreachable target
	// is an environment condition.
	ErrExternalVersionUnsupported    = &UpgradeError{Code: UpgradeErrorExternalVersionUnsupported}
	ErrExternalConfigDocumentInvalid = &UpgradeError{Code: UpgradeErrorExternalConfigDocumentInvalid}
	ErrExternalManagementRejected    = &UpgradeError{Code: UpgradeErrorExternalManagementRejected}
	ErrExternalTargetMoved           = &UpgradeError{Code: UpgradeErrorExternalTargetMoved}
	ErrExternalAuthorityUnavailable  = &UpgradeError{Code: UpgradeErrorExternalAuthorityUnavailable}
	ErrExternalNotApplicable         = &UpgradeError{Code: UpgradeErrorExternalNotApplicable}
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
