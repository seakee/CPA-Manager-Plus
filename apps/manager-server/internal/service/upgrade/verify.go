package upgrade

// Verify is the one completion gate of the v2 Upgrade operation. It is the only
// production caller of upgradeoperation.RecordCompletionTx, and it is shared by
// the CLI and the Upgrade wizard, so the shell an operator uses cannot disagree
// with the page about whether an operation finished.
//
// # What it proves
//
// In one target write transaction it re-derives, from the durable record and
// nothing the caller supplied:
//
//   - the applicable operation domains are done: the copy-in domain, every domain
//     the persisted reconciliation established, and — for an External target —
//     the two domains the connect step records. The set is derived, not fixed:
//     this build never records `usage_events`, so requiring the whole model
//     vocabulary would make every real operation unverifiable;
//   - the persisted reconciliation still passes the contract the reconcile step
//     applies: it is the evidence object, it established at least one domain,
//     every domain that did not apply says why, no day is left undecided, every
//     day it marked incomplete is acknowledged, and every mark it recorded is
//     still on disk. An acknowledgement is an accepted loss, never a waiver;
//   - the 1.x source is the volume the operation copied: its manifest digest
//     equals the recorded one, was observed under the read-only source ownership
//     fence, and is unchanged around the write;
//   - the target is what the Manager itself stores: an External operation's
//     upgrade_external_connect binding is read in the same transaction and
//     compared with the Manager's own CPA connection, and a Managed operation's
//     imported destination connection is still the one the import recorded;
//   - a Managed destination is the one the live Runtime Supervisor runs: the
//     active gateway artifact it reports is the artifact this build's adoption
//     record accepted and the one the stage request named, and its identity is
//     the identity the attempt was staged under. The CPA at the recorded address
//     answers the same reads whichever artifact it is, so this observation is the
//     only thing that distinguishes the staged destination from a later one.
//
// # The two outcomes
//
// A target that passes is completed: the verification evidence and the complete
// stage commit in one statement. Both target modes are completed by this gate.
// An External target proves the binding the connect step recorded; a Managed
// target proves the destination the CPA import recorded — its address, the
// Manager's sealed credential for it, and the live v8 server fingerprint — so the
// Manager that starts after the upgrade administers the CPA this run verified.
// Every manual item and refusal earlier steps recorded stays exactly as it was.
// Neither outcome is invented: a failure of any check writes nothing and returns
// a typed error.
//
// # What it refuses
//
// A source that is not mounted cannot be fingerprinted, so it is a typed refusal
// instead of a verification that skipped the volume. A store another process
// owns is a typed refusal, never a second writer. A rolled_back operation is
// terminal and is never resumed. Repeating the call on a completed operation
// returns the recorded verdict without writing — but only after re-checking that
// verdict against the credential and target the Manager itself stores, because a
// completion this installation cannot administer is not a finished install. Nothing
// in this file rewrites the recorded source observations or the target binding.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/coverageincomplete"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

// VerifyOptions is one invocation of the completion gate.
type VerifyOptions struct {
	// LegacyRoot is the stopped 1.x data directory the operation copied. It is
	// required: the source fingerprint is the observation that says the volume
	// was not written, and it cannot be taken without the volume.
	LegacyRoot string
	// TargetRoot is the v2 data directory holding usage.sqlite and the copy-in
	// work area.
	TargetRoot string
	// ScratchDir holds private copies outside both preserved roots. Empty uses
	// the OS temporary directory, subject to the same two-root validation.
	ScratchDir string

	// The four seams below are the ones ExternalConnectOptions exposes. Production
	// leaves them nil and the cpa package's probes run on the External transport,
	// so the gate proves the recorded target with the same reads the connect step
	// made. Both target modes use them: a Managed destination is the same v8
	// management surface, and the gate proves it live rather than trusting the
	// import's own observation. A test supplies them so it can prove the refused
	// cases without a network.
	observeExternalVersion func(ctx context.Context, baseURL, managementKey string) (cpa.ExternalVersionReport, error)
	readExternalKeys       func(ctx context.Context, baseURL, managementKey string) (cpa.ExternalKeySnapshot, error)
	proveModelSurface      func(ctx context.Context, baseURL, clientKey string) error
	readV8Config           func(ctx context.Context, baseURL, managementKey string) (cpa.V8ConfigObservation, error)
	// Runtime is the Manager-owned Supervisor the Managed destination was staged
	// on. Production leaves it nil and the client is resolved from the Manager's
	// own configuration; a caller that already holds the client — a test, or a
	// handler built with one — supplies it, so the gate asks the Supervisor the
	// Manager is running rather than a second client built from the same
	// environment.
	Runtime VerifyRuntime
}

// VerifyRuntime is the Supervisor observation the Managed gate needs: the
// artifact it is running, the identity it runs under and the state of the CPA it
// started. It is the read half of the import's own Runtime seam, so the gate and
// the import observe the same authority.
type VerifyRuntime interface {
	Status(ctx context.Context) (model.RuntimeObservedStatus, error)
}

// VerifyResult is the observable outcome of the gate. It reports what the row
// now says, never an authority: Complete is true only for a persisted complete
// row, and ReasonCode/Reason are the recorded position of a Managed operation
// this build may not promote.
type VerifyResult struct {
	Stage       model.UpgradeStage `json:"stage"`
	DomainsDone []string           `json:"domainsDone"`
	// Complete is the persisted verdict: this run completed the operation, or the
	// completed row it found already said so.
	Complete bool `json:"complete"`
	// TargetMode is the mode the gate derived from the durable record and the
	// Manager's own configuration: "external" or "managed".
	TargetMode string `json:"targetMode"`
	// ReasonCode and Reason are the typed position of a Managed operation. They
	// are empty for a completed one.
	ReasonCode string `json:"reasonCode,omitempty"`
	Reason     string `json:"reason,omitempty"`
	// ManualItems is the recorded instruction list after this run. A completion
	// keeps it; a Managed verification appends its one line to it.
	ManualItems []string `json:"manualItems"`
	// AlreadyVerified reports that the row already held this exact verdict and
	// nothing was written.
	AlreadyVerified bool  `json:"alreadyVerified"`
	UpdatedAtMS     int64 `json:"updatedAtMs"`
}

// Verify proves the recorded operation and records the one verdict it may reach.
func Verify(ctx context.Context, options VerifyOptions) (VerifyResult, error) {
	if err := ctx.Err(); err != nil {
		return VerifyResult{}, err
	}
	targetRoot, err := resolveTargetRoot(options.TargetRoot)
	if err != nil {
		return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
			"the v2 data root is not usable, so there is no recorded operation to verify; nothing was written", err)
	}
	sourceRoot := strings.TrimSpace(options.LegacyRoot)
	if sourceRoot == "" {
		// The fingerprint is the proof that the volume the operation copied was
		// not written since. A gate that skipped it would claim an observation it
		// never took, so an absent mount is a refusal.
		return VerifyResult{}, newUpgradeError(UpgradeErrorVerifySourceUnavailable,
			"no 1.x data directory is mounted, so the source the operation copied cannot be observed; the verification needs the volume to prove it was not written, and nothing was written", nil)
	}
	paths := newTargetPaths(targetRoot)
	// The 1.x volume is a read-only input, so no root may contain another: a
	// source inside the v2 data root would put the verification's own writes in
	// the tree it fingerprints.
	if err := RefuseOverlappingRoots(sourceRoot, targetRoot, DefaultBundleDirectory(targetRoot)); err != nil {
		return VerifyResult{}, err
	}
	if err := requireRecoveredCopyIn(paths.Database); err != nil {
		return VerifyResult{}, err
	}
	if err := ValidateScratchDir(options.ScratchDir, sourceRoot, targetRoot); err != nil {
		return VerifyResult{}, snapshotReadError(ctx, err)
	}
	recorded, found, snapshot, err := readCopyInCandidate(ctx, paths.Database, sourceRoot, targetRoot, options.ScratchDir)
	if snapshot != nil {
		defer func() { _ = snapshot.Close() }()
	}
	if err != nil {
		return VerifyResult{}, verifyRecordReadError(snapshotReadError(ctx, err))
	}
	if !found {
		return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
			"the v2 data root holds no recorded copy-in, so there is no operation to verify; nothing was written", nil)
	}
	// A completed operation is over: the gate reports the stored verdict and
	// touches neither the store nor the source. The product is admitted normally
	// from here, so it must not need the 1.x volume to answer this call.
	if recorded.Stage == model.UpgradeStageComplete {
		return completedVerifyResultFromStore(ctx, snapshot.DB(), paths, recorded)
	}
	if err := verifyEntry(recorded); err != nil {
		return VerifyResult{}, err
	}

	// Two fences for the whole run: the 1.x source is read only, so its ownership
	// marker is taken through a read-only descriptor (the same exclusive marker,
	// never a write to a preserved volume), and the v2 data root is the store this
	// step writes, so it takes the same ownership lock a running Manager holds.
	// Taking them before the source is read is what makes "unchanged" meaningful:
	// a cooperating writer cannot slip between the two fingerprints.
	sourceLock, err := acquireVerifySourceFence(sourceRoot)
	if err != nil {
		return VerifyResult{}, err
	}
	defer func() { _ = sourceLock.Close() }()
	targetLock, err := acquireVerifyTargetFence(paths.Database)
	if err != nil {
		return VerifyResult{}, err
	}
	defer func() { _ = targetLock.Close() }()

	if err := requireRecoveredCopyIn(paths.Database); err != nil {
		return VerifyResult{}, err
	}
	db, err := managersqlite.Open(paths.Database)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("open the v2 database for the verification: %w", err)
	}
	defer func() { _ = db.Close() }()
	repository := upgradeoperation.New(db)
	// The authoritative read happens under the fence: the row the transaction will
	// write into is the one below, not the pre-fence snapshot.
	recorded, found, err = readRecordedCopyInFrom(ctx, repository)
	if err != nil {
		return VerifyResult{}, verifyRecordReadError(err)
	}
	if !found {
		return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
			"the v2 data root holds no recorded operation, so there is nothing to verify; nothing was written", nil)
	}
	if recorded.Stage == model.UpgradeStageComplete {
		binding, hasBinding, err := repository.ExternalBinding(ctx)
		if err != nil {
			return VerifyResult{}, fmt.Errorf("read the External connect binding: %w", err)
		}
		return completedVerifyResultForStore(ctx, db, paths, recorded, binding, hasBinding)
	}
	if err := verifyEntry(recorded); err != nil {
		return VerifyResult{}, err
	}

	binding, hasBinding, err := repository.ExternalBinding(ctx)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("read the External connect binding: %w", err)
	}
	mode, err := verifyTargetMode(recorded, hasBinding)
	if err != nil {
		return VerifyResult{}, err
	}
	if err := requireVerifyEntryForMode(recorded, mode); err != nil {
		return VerifyResult{}, err
	}
	// Everything the target mode rests on is read under the fence and before the
	// transaction: the Manager's own configuration and the imported destination
	// record are not the operation row, and the fence is what proves no other
	// writer can change them while this run decides.
	target, err := verifyTargetConfiguration(ctx, db, paths, mode)
	if err != nil {
		return VerifyResult{}, err
	}
	if err := requireVerifyTarget(mode, binding, target); err != nil {
		return VerifyResult{}, err
	}
	// The stored-key equality above proves the Manager holds the credential the
	// target step proved. This proves that credential still administers the CPA
	// right now: the same reads the connect step made, made with the secret the
	// Manager itself will present after a restart. It runs before the write
	// transaction, so a refused live proof leaves the operation row unadvanced.
	switch mode {
	case model.UpgradeTargetExternal:
		if err := proveVerifyExternalTarget(ctx, options, target, binding); err != nil {
			return VerifyResult{}, err
		}
	case model.UpgradeTargetManaged:
		if err := proveVerifyManagedTarget(ctx, options, db, target); err != nil {
			return VerifyResult{}, err
		}
	}

	before, err := fingerprintSource(ctx, sourceRoot)
	if err != nil {
		return VerifyResult{}, newUpgradeError(UpgradeErrorVerifySourceUnavailable,
			"the mounted 1.x directory could not be inspected, so the source cannot be proven unchanged; nothing was written", err)
	}
	if digest := strings.TrimSpace(recorded.SourceFingerprintAfter); digest == "" {
		return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyReconciliationInvalid,
			"the recorded copy-in names no source fingerprint, so the mounted 1.x volume cannot be proven to be the one that was copied; nothing was written", nil)
	} else if digest != before.Digest {
		return VerifyResult{}, newUpgradeError(UpgradeErrorVerifySourceMismatch,
			"the mounted 1.x source is not the volume the recorded copy-in copied; nothing was written", nil)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("begin the verification transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, found, err := upgradeoperation.GetTx(ctx, tx)
	if err != nil {
		return VerifyResult{}, verifyRecordReadError(err)
	}
	if !found {
		return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
			"the recorded operation disappeared before it could be verified; nothing was written", nil)
	}
	if current.Stage == model.UpgradeStageComplete {
		// The binding is read inside the transaction: a completed verdict is
		// reported only against the target record this transaction sees.
		binding, hasBinding, err := upgradeoperation.ExternalBindingTx(ctx, tx)
		if err != nil {
			return VerifyResult{}, fmt.Errorf("read the External connect binding: %w", err)
		}
		return completedVerifyResultForStore(ctx, db, paths, current, binding, hasBinding)
	}
	if err := verifyEntry(current); err != nil {
		return VerifyResult{}, err
	}
	// The binding is read again inside the transaction: the row that carries the
	// verdict must name the target this transaction saw, not the one an earlier
	// read found.
	binding, hasBinding, err = upgradeoperation.ExternalBindingTx(ctx, tx)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("read the External connect binding: %w", err)
	}
	if txMode, modeErr := verifyTargetMode(current, hasBinding); modeErr != nil {
		return VerifyResult{}, modeErr
	} else if txMode != mode {
		return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyBindingDrifted,
			"the recorded target changed while it was being verified, so the verdict cannot name the connection it rests on; nothing was written", upgradeoperation.ErrConflict)
	}
	if err := requireVerifyEntryForMode(current, mode); err != nil {
		return VerifyResult{}, err
	}

	_, established, err := verifyReconciliation(ctx, tx, current, before.Digest)
	if err != nil {
		return VerifyResult{}, err
	}
	required := verifyRequiredDomains(mode, established)
	if err := requireVerifyDomains(current, required); err != nil {
		return VerifyResult{}, err
	}
	if err := requireVerifyTarget(mode, binding, target); err != nil {
		return VerifyResult{}, err
	}

	verification := model.UpgradeVerification{
		TargetMode:        mode,
		SourceFingerprint: before.Digest,
		Domains:           required,
		ObservedAtMS:      time.Now().UnixMilli(),
	}
	if mode == model.UpgradeTargetExternal {
		verification.BindingIdentity = binding.Identity
		verification.BindingGeneration = binding.Generation
		verification.BindingObservedAtMS = binding.ObservedAtMS
	}
	var operation model.UpgradeOperation
	// The revision this transaction saw is the token the verdict's statement
	// carries. It is also what tells a repeat apart from a write: the writer
	// stores nothing when it re-proves the verdict already on the row, so an
	// unchanged revision means this run observed and recorded nothing new.
	previousRevision := current.Revision
	// Both target modes complete here. The target each one proves is named by the
	// evidence this statement writes; there is no waiting position any more,
	// because both proofs now exist.
	operation, err = upgradeoperation.RecordCompletionTx(ctx, tx, verification)
	if err != nil {
		return VerifyResult{}, mapVerifyRecordError(err)
	}
	already := operation.Revision == previousRevision
	// The second observation happens with the transaction still open, so a source
	// that was written during the run aborts the verdict instead of being
	// recorded as unchanged. Nothing has been committed at this point.
	after, err := fingerprintSource(ctx, sourceRoot)
	if err != nil {
		return VerifyResult{}, newUpgradeError(UpgradeErrorVerifySourceUnavailable,
			"the mounted 1.x directory could not be inspected again, so the source cannot be proven unchanged; the verdict was not recorded", err)
	}
	if after != before {
		// The volume the operation copied was written while this run held its
		// ownership marker, so the observation no longer describes it. The source
		// is stopped by contract; a writer is a decision for the operator.
		return VerifyResult{}, newUpgradeError(UpgradeErrorVerifySourceMismatch,
			"the 1.x source changed while the operation was being verified, so the observation no longer describes the volume the copy-in made; nothing was recorded", nil)
	}
	if err := tx.Commit(); err != nil {
		return VerifyResult{}, fmt.Errorf("record the verification: %w", err)
	}
	return verifyResult(operation, mode, already), nil
}

// completedVerifyResult reports a row that already carries the verdict. Nothing
// is written and the source is not read: the operation is over, the product is
// admitted normally, and a repeated call must not re-observe a volume those
// installs may already have discarded.
//
// The stage is not the verdict on its own. This reader re-checks the record the
// gate wrote against the row it was written to: an External target, the source
// volume the copy-in observed, every domain the verdict names, and the target
// binding that verdict proved: the External connect row for an External target,
// the sealed credential and the destination the import handed over for a Managed
// one. A row that fails any of them is a store whose `complete` label no gate ever
// proved, and it is reported as unusable instead of as a finished install.
func completedVerifyResult(recorded model.UpgradeOperation, binding upgradeoperation.ExternalBinding, hasBinding bool) (VerifyResult, error) {
	// The stage alone is not a verdict. The row must still describe the copy-in the
	// gate observed — the upgraded-legacy label, both source fingerprints and the
	// backup manifest digest — because without them this store cannot be told apart
	// from any other database that happens to carry a `complete` row. This is the
	// same set the startup admission requires before it unlocks the normal surfaces.
	if recorded.SourceKind != SourceKindUpgradedLegacy {
		return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
			"the recorded operation is complete but does not name the upgraded-legacy source kind, so its verdict cannot be reported; restore the v2 store from its backup", nil)
	}
	if strings.TrimSpace(recorded.SourceFingerprintBefore) == "" || strings.TrimSpace(recorded.SourceFingerprintAfter) == "" {
		return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
			"the recorded operation is complete but does not carry both source fingerprints, so the volume its verdict describes cannot be identified; restore the v2 store from its backup", nil)
	}
	if strings.TrimSpace(recorded.BackupManifestSHA256) == "" {
		return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
			"the recorded operation is complete but does not carry the backup manifest digest, so the copy it claims cannot be identified; restore the v2 store from its backup", nil)
	}
	evidence, err := recorded.VerificationEvidence()
	if err != nil {
		return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
			"the recorded operation is complete but carries no verification evidence, so its verdict cannot be reported; restore the v2 store from its backup", err)
	}
	if !evidence.Complete() {
		return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
			"the recorded operation is complete but its verification evidence is incomplete, so its verdict cannot be reported; restore the v2 store from its backup", nil)
	}
	if strings.TrimSpace(evidence.SourceFingerprint) != strings.TrimSpace(recorded.SourceFingerprintAfter) {
		return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
			"the recorded operation is complete but its verification names a different source volume than the recorded copy-in, so its verdict cannot be reported; restore the v2 store from its backup", nil)
	}
	required := append([]string{}, evidence.Domains...)
	requiredDomains := []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig}
	if evidence.TargetMode == model.UpgradeTargetExternal {
		requiredDomains = append(requiredDomains, model.UpgradeDomainIdentity)
	}
	for _, domain := range requiredDomains {
		if !containsVerifyDomain(required, domain) {
			return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable, fmt.Sprintf(
				"the recorded operation is complete but its verification does not name the %q domain this target needs, so its verdict cannot be reported; restore the v2 store from its backup", domain), nil)
		}
	}
	missing := make([]string, 0, len(required))
	for _, domain := range required {
		if !recorded.HasDomain(domain) {
			missing = append(missing, domain)
		}
	}
	if len(missing) > 0 {
		return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable, fmt.Sprintf(
			"the recorded operation is complete but does not hold the %s its verification proves, so its verdict cannot be reported; restore the v2 store from its backup: %s",
			plural(len(missing), "domain", "domains"), strings.Join(missing, ", ")), nil)
	}
	switch evidence.TargetMode {
	case model.UpgradeTargetExternal:
		if !hasBinding {
			return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
				"the recorded operation is complete but carries no External connect binding, so the target its verdict names cannot be checked; restore the v2 store from its backup", nil)
		}
		if !security.IsValidProtectedEnvelope(strings.TrimSpace(binding.ManagementKey)) {
			// The verdict proves the binding, never the credential the connect step
			// proved against it. A row completed before that secret was sealed carries
			// none, and UnprotectString would hand an unsealed value back as if it
			// were readable, so the shape is checked before anything decrypts it.
			return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
				"the recorded operation is complete but its External binding carries no sealed management secret, so the credential the Manager would present was never proved; restore the v2 store from its backup", nil)
		}
		if evidence.BindingIdentity != binding.Identity ||
			evidence.BindingGeneration != binding.Generation ||
			evidence.BindingObservedAtMS != binding.ObservedAtMS {
			return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
				"the recorded operation is complete but its verification names a different External binding than the recorded one, so its verdict cannot be reported; restore the v2 store from its backup", nil)
		}
	case model.UpgradeTargetManaged:
		// A Managed target has no External binding. A stored one means the row's
		// mode is a claim the store contradicts, so the verdict is unusable.
		if hasBinding {
			return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
				"the recorded operation is complete as a Managed target but carries an External connect binding, so the target its verdict names cannot be checked; restore the v2 store from its backup", nil)
		}
	}
	return verifyResult(recorded, evidence.TargetMode, true), nil
}

// readExternalBindingFrom reads the recorded External binding from an open
// connection. A store whose connect step never ran, or one written before the
// binding table existed, simply has none.
func readExternalBindingFrom(ctx context.Context, db *sql.DB) (upgradeoperation.ExternalBinding, bool, error) {
	present, err := tablePresent(ctx, db, "upgrade_external_connect")
	if err != nil || !present {
		return upgradeoperation.ExternalBinding{}, false, err
	}
	return upgradeoperation.New(db).ExternalBinding(ctx)
}

// completedVerifyResultFromStore answers a completed operation from a read-only
// handle: the stored verdict, then the target the Manager itself stores.
func completedVerifyResultFromStore(ctx context.Context, db *sql.DB, paths targetPaths, recorded model.UpgradeOperation) (VerifyResult, error) {
	binding, hasBinding, err := readExternalBindingFrom(ctx, db)
	if err != nil {
		return VerifyResult{}, fmt.Errorf("read the External connect binding: %w", err)
	}
	return completedVerifyResultForStore(ctx, db, paths, recorded, binding, hasBinding)
}

// completedVerifyResultForStore is the completed-row contract: the stored
// verdict first, then the credential and target it rests on.
//
// A repeat of the gate on a completed operation used to report the verdict from
// the row alone, so a row that reached `complete` before the connect step sealed
// its secret was answered as a finished install even though the Manager would
// meet 401 on its next start. The stored connection is therefore compared here
// too, from the same read-only handle: nothing is written either way.
func completedVerifyResultForStore(ctx context.Context, db *sql.DB, paths targetPaths, recorded model.UpgradeOperation,
	binding upgradeoperation.ExternalBinding, hasBinding bool) (VerifyResult, error) {
	result, err := completedVerifyResult(recorded, binding, hasBinding)
	if err != nil {
		return VerifyResult{}, err
	}
	switch result.TargetMode {
	case model.UpgradeTargetExternal:
		target, err := verifyTargetConfiguration(ctx, db, paths, model.UpgradeTargetExternal)
		if err != nil {
			return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
				"the recorded operation is complete but the CPA connection its verdict rests on cannot be read, so its verdict cannot be reported; restore the v2 store from its backup", err)
		}
		if err := requireVerifyTarget(model.UpgradeTargetExternal, binding, target); err != nil {
			return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
				"the recorded operation is complete but the credential its verdict proved is not the one the Manager stores, so its verdict cannot be reported; restore the v2 store from its backup", err)
		}
	case model.UpgradeTargetManaged:
		target, err := verifyTargetConfiguration(ctx, db, paths, model.UpgradeTargetManaged)
		if err != nil {
			return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
				"the recorded operation is complete but the imported destination its verdict rests on cannot be read, so its verdict cannot be reported; restore the v2 store from its backup", err)
		}
		if err := requireVerifyTarget(model.UpgradeTargetManaged, binding, target); err != nil {
			return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
				"the recorded operation is complete but the destination credential its verdict proved is not the one the Manager stores, so its verdict cannot be reported; restore the v2 store from its backup", err)
		}
		if err := verifyStoredManagedConnection(ctx, db, target); err != nil {
			return VerifyResult{}, newUpgradeError(UpgradeErrorVerifyNotApplicable,
				"the recorded operation is complete but the Manager's sealed CPA connection cannot administer the destination its verdict proved, so its verdict cannot be reported; restore the v2 store from its backup", err)
		}
	}
	return result, nil
}

// verifyRecordReadError maps the one stored-row failure the gate cannot report as
// anything but an outdated verdict: a complete row whose verification did not
// survive. Keeping it in the gate's own vocabulary means a caller reads the same
// code for a completion this run refused and one the store cannot describe.
func verifyRecordReadError(err error) error {
	if errors.Is(err, upgradeoperation.ErrVerificationEvidenceMissing) {
		return newUpgradeError(UpgradeErrorVerifyNotApplicable,
			"the recorded operation is complete but carries no verification evidence, so its verdict cannot be reported; restore the v2 store from its backup", err)
	}
	return err
}

func verifyResult(operation model.UpgradeOperation, mode string, already bool) VerifyResult {
	result := VerifyResult{
		Stage:           operation.Stage,
		DomainsDone:     append([]string{}, operation.DomainsDone...),
		Complete:        operation.Stage == model.UpgradeStageComplete,
		TargetMode:      mode,
		ManualItems:     append([]string{}, operation.ManualItems...),
		AlreadyVerified: already,
		UpdatedAtMS:     operation.UpdatedAtMS,
	}
	if !result.Complete {
		result.ReasonCode = operation.ErrorCode
		result.Reason = operation.ErrorDetail
	}
	return result
}

// verifyEntry proves the recorded position is one the gate may run from at all.
// The terminal verdict and the stages before reconciliation are refused here;
// which target step the operation must have passed is decided per mode.
func verifyEntry(recorded model.UpgradeOperation) error {
	if recorded.Stage == model.UpgradeStageRolledBack {
		return newUpgradeError(UpgradeErrorVerifyNotApplicable,
			"the recorded operation was rolled back, and the terminal verdict is never resumed; nothing was written", nil)
	}
	switch recorded.Stage {
	case model.UpgradeStageReconcile, model.UpgradeStageCPAImport,
		model.UpgradeStageExternalConnect, model.UpgradeStageVerify:
		return nil
	case model.UpgradeStageFailed:
		if recorded.HighWater() == "" {
			return newUpgradeError(UpgradeErrorVerifyNotApplicable,
				"the recorded operation failed before any forward stage was reached, so the position it may resume from is unknown; nothing was written", nil)
		}
		return nil
	}
	return newUpgradeError(UpgradeErrorVerifyNotApplicable, fmt.Sprintf(
		"the recorded operation is at stage %q, which cannot enter the verification; nothing was written", recorded.Stage), nil)
}

// requireVerifyEntryForMode proves the target step the mode needs already ran: an
// External target requires the connection it recorded, and a Managed target the
// import that handed the destination over. Verification of a step the pipeline
// never reached would claim work nothing performed.
func requireVerifyEntryForMode(recorded model.UpgradeOperation, mode string) error {
	position := recorded.HighWater()
	required := model.UpgradeStageCPAImport
	if mode == model.UpgradeTargetExternal {
		required = model.UpgradeStageExternalConnect
	}
	if position == required || position == model.UpgradeStageVerify || position == model.UpgradeStageComplete {
		return nil
	}
	return newUpgradeError(UpgradeErrorVerifyNotApplicable, fmt.Sprintf(
		"the recorded operation is at stage %q, before the %q step this %s target must record; nothing was written",
		recorded.Stage, required, mode), nil)
}

// verifyTargetMode derives the target mode from the durable record. The External
// connection step writes the binding in the same transaction as its stage, so a
// binding is the record of an External target and its absence the record of a
// Managed one. A row that reached the connect stage without its binding is
// refused instead of being read as Managed: the CPA it connected cannot be named.
func verifyTargetMode(recorded model.UpgradeOperation, hasBinding bool) (string, error) {
	if hasBinding {
		return model.UpgradeTargetExternal, nil
	}
	if recorded.Stage == model.UpgradeStageExternalConnect || recorded.HighWater() == model.UpgradeStageExternalConnect {
		return "", newUpgradeError(UpgradeErrorVerifyBindingMissing,
			"the recorded operation is at external_connect but carries no target binding, so the CPA it connected cannot be proven; nothing was written", nil)
	}
	return model.UpgradeTargetManaged, nil
}

// verifyTarget is what the Manager itself says about the CPA it uses. It is read
// from the store, never from a caller's flag: the connection the next start will
// resolve is the only authority on which target this installation has.
type verifyTarget struct {
	// connection is the Manager's own CPA connection, when the store holds one.
	connection managerconfig.LegacyConnection
	found      bool
	// protector is the Manager secret protector the connection was read with. It
	// decrypts the sealed secret the External connect step recorded, so the two
	// credentials can be compared without either being re-derived from a caller.
	protector *security.Protector
	// attempt is the destination the CPA import recorded for a Managed target.
	attempt       cpaImportDestinationRecord
	attemptSecret string
	attemptFound  bool
}

// verifyTargetConfiguration reads the Manager's own CPA connection and, for a
// Managed target, the destination attempt the import recorded with its secret.
// Both reads happen under the v2 data-root fence.
func verifyTargetConfiguration(ctx context.Context, db *sql.DB, paths targetPaths, mode string) (verifyTarget, error) {
	target := verifyTarget{}
	protector, err := verifyTargetProtector(paths)
	if err != nil {
		return target, err
	}
	st := store.New(db, protector)
	managerCfg, managerOK, err := st.LoadManagerConfig(ctx)
	if err != nil {
		return target, newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the Manager configuration could not be read, so the CPA this installation uses cannot be confirmed; nothing was written", err)
	}
	setup, setupOK, err := st.LoadSetup(ctx)
	if err != nil {
		return target, newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the stored Manager configuration could not be read, so the CPA this installation uses cannot be confirmed; nothing was written", err)
	}
	resolution, err := managerconfig.ResolveLegacyConnectionAuthority(managerCfg, managerOK, setup, setupOK)
	code := UpgradeErrorVerifyBindingDrifted
	if mode == model.UpgradeTargetManaged {
		code = UpgradeErrorVerifyConnectionUnproven
	}
	if err != nil {
		return target, newUpgradeError(code,
			"the Manager stores two CPA connections that disagree, so the target this installation uses cannot be confirmed; nothing was written", err)
	}
	switch resolution.Authority {
	case managerconfig.LegacyConnectionAuthorityNone:
		return target, newUpgradeError(code,
			"the Manager stores no CPA connection, so the target this installation uses cannot be confirmed; nothing was written", nil)
	}
	target.connection = resolution.Connection
	target.found = true
	target.protector = protector
	if mode != model.UpgradeTargetManaged {
		return target, nil
	}
	attempt, secret, found, err := loadCPAImportDestinationRecordFrom(ctx, db, paths.DataKey)
	if err != nil {
		return target, newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the recorded destination connection could not be read, so the import cannot be confirmed to have survived; nothing was written", err)
	}
	target.attempt, target.attemptSecret, target.attemptFound = attempt, secret, found
	return target, nil
}

// verifyTargetProtector loads the data key the Manager store uses to protect its
// stored secrets. It never creates one: a verify that minted a key could not
// decrypt the envelope it is looking at and would leave a second key behind.
func verifyTargetProtector(paths targetPaths) (*security.Protector, error) {
	key, err := security.LoadExistingDataKey("", paths.DataKey)
	if err != nil {
		return nil, newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the Manager data key is missing, so the stored CPA connection cannot be read; nothing was written", err)
	}
	protector, err := security.NewProtector(key)
	if err != nil {
		return nil, newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the Manager secret protector could not be initialized, so the stored CPA connection cannot be read; nothing was written", err)
	}
	return protector, nil
}

// requireVerifyTarget compares the target the operation recorded with the target
// the Manager itself stores.
//
// External: the binding must name the address the Manager's own connection
// names, by the same identity the connect step recorded. A repointed
// configuration is a moved target, not a verification, so it is refused.
//
// Managed: the import's recorded destination must have been proved by a run, and
// the Manager's connection must still name that destination with the same
// management secret. That is connection preservation: the handoff survived, and
// nothing compared here is a caller's claim.
func requireVerifyTarget(mode string, binding upgradeoperation.ExternalBinding, target verifyTarget) error {
	if !target.found {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the Manager stores no CPA connection, so the target this installation uses cannot be confirmed; nothing was written", nil)
	}
	if mode == model.UpgradeTargetExternal {
		canonical, err := canonicalVerifyAddress(target.connection.BaseURL)
		if err != nil {
			return newUpgradeError(UpgradeErrorVerifyBindingDrifted,
				"the CPA address the Manager stores is not a usable endpoint, so the recorded connection cannot be compared with it; nothing was written", err)
		}
		if canonical != binding.BaseURL || freshkeyapply.ExternalIdentity(canonical) != binding.Identity {
			return newUpgradeError(UpgradeErrorVerifyBindingDrifted,
				"the CPA the Manager connects to is not the target the External connect step recorded; nothing was written", nil)
		}
		// The address is not the connection. The Manager presents the credential
		// its own store holds, so a completion has to prove that credential is the
		// one the connect step proved against this CPA. Comparing only the address
		// recorded a verdict for a Manager that would meet 401 on its next start.
		if !cpa.ValidV8ManagementKey(strings.TrimSpace(target.connection.ManagementKey)) {
			return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
				"the Manager's stored CPA connection carries no usable management secret, so the External target it names cannot be administered; nothing was written", nil)
		}
		if strings.TrimSpace(binding.ManagementKey) == "" {
			return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
				"the External connect record carries no proved management secret, so the credential the Manager would present cannot be confirmed; run the External connect step again with this build, and nothing was written", nil)
		}
		if !security.IsValidProtectedEnvelope(strings.TrimSpace(binding.ManagementKey)) {
			// UnprotectString returns a value that is not an envelope as-is, so a
			// plaintext secret would compare equal to the Manager's own plaintext
			// and be reported as a proved credential. Only a sealed value is one.
			return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
				"the External connect record does not carry a sealed management secret, so the credential the Manager would present cannot be confirmed; run the External connect step again with this build, and nothing was written", nil)
		}
		if target.protector == nil {
			return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
				"the Manager data key is unavailable, so the management secret the External connect step proved cannot be read; nothing was written", nil)
		}
		bindingSecret, err := target.protector.UnprotectString(binding.ManagementKey)
		if err != nil {
			return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
				"the management secret the External connect step proved cannot be read with this Manager data key, so the stored connection cannot be compared with it; nothing was written", err)
		}
		if !security.EqualHMAC(strings.TrimSpace(target.connection.ManagementKey), strings.TrimSpace(bindingSecret)) {
			return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
				"the Manager connection does not carry the management secret the External connect step proved, so a restarted Manager could not administer the CPA this run verified; run `upgrade external-connect --reprove` with the current management key to re-prove the CPA and seal it, or `store-cpa-connection` with the key the External connect step proved, and nothing was written", nil)
		}
		return nil
	}
	if !target.attemptFound {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the CPA import recorded no destination attempt, so the connection it handed over cannot be confirmed; nothing was written", nil)
	}
	if err := requireManagedDestinationEvidence(target.attempt); err != nil {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the CPA import's recorded destination does not describe a proved server, so the connection it handed over cannot be confirmed; nothing was written", err)
	}
	// The address comparison is canonical, by the same two functions the External
	// arm and the startup admission apply: "127.0.0.1:8317" and
	// "http://127.0.0.1:8317/" name one endpoint, and comparing spellings would
	// refuse a handoff that is intact.
	destinationAddress, err := canonicalVerifyAddress(target.attempt.BaseURL)
	if err != nil {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the destination the CPA import recorded is not a usable endpoint, so the connection it handed over cannot be confirmed; nothing was written", err)
	}
	connectionAddress, err := canonicalVerifyAddress(target.connection.BaseURL)
	if err != nil {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the CPA address the Manager stores is not a usable endpoint, so the connection the CPA import handed over cannot be confirmed; nothing was written", err)
	}
	if destinationAddress != connectionAddress {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the Manager connection no longer names the destination the CPA import recorded, so the handoff did not survive; nothing was written", nil)
	}
	if !security.EqualHMAC(strings.TrimSpace(target.connection.ManagementKey), strings.TrimSpace(target.attemptSecret)) {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the Manager connection no longer carries the management secret the CPA import handed over; nothing was written", nil)
	}
	return nil
}

// requireManagedDestinationEvidence proves the recorded destination row is the
// whole evidence a Managed handoff rests on: a proved address, the source
// identity and class the destination was staged for, the Supervisor authority and
// artifact the stage request named, the client keys it published, the plugin
// choice the operator made, and the server fingerprint a run observed with the
// authenticated proof. A row missing any of them describes a destination no run
// of this operation proved, so the live re-proof has nothing to compare against.
//
// The provenance is checked field by field rather than by presence, because the
// handoff is the claim that this operation staged this destination: the attempt
// identity is derived from the source it was staged for, and the request is
// replayed by the Supervisor against the authority and artifact recorded here. A
// row whose operation ID does not resolve to its own source and attempt, or whose
// recorded artifact is not the one this build's adoption record was qualified
// against, describes a stage this operation never submitted.
//
// The fingerprint is checked by shape, exactly as the admission checks it: a value
// that is not the digest freshkeyapply.ExternalServerFingerprint produces can
// never equal the one the gate derives from the live CPA, so reporting it as
// evidence would be a comparison that always fails for the wrong reason.
func requireManagedDestinationEvidence(destination cpaImportDestinationRecord) error {
	if strings.TrimSpace(destination.BaseURL) == "" {
		return errors.New("the recorded destination names no address")
	}
	if !destination.urlProved() {
		return errors.New("the recorded destination names no address a run proved")
	}
	if !validManagedServerFingerprint(destination.ServerFingerprint) {
		return errors.New("the recorded destination names no proved server fingerprint")
	}
	intent := destination.Intent
	if !validManagedSourceIdentity(intent.SourceIdentity) {
		return errors.New("the recorded destination names no source identity this import stamps")
	}
	if intent.SourceClass != managedDestinationSourceClass {
		return errors.New("the recorded destination does not name the legacy managed source class the import stages")
	}
	if destination.Attempt < 1 {
		return errors.New("the recorded destination names no attempt identity")
	}
	if destination.OperationID != cpaImportDestinationOperationID(intent.SourceIdentity, destination.Attempt) {
		return errors.New("the recorded destination's operation ID does not resolve to its own source and attempt")
	}
	if strings.TrimSpace(string(intent.ExpectedRuntimeIdentity)) == "" || intent.ExpectedRuntimeGeneration == 0 {
		return errors.New("the recorded destination names no Supervisor authority the stage request addressed")
	}
	if len(intent.APIKeys) == 0 {
		return errors.New("the recorded destination names no client key list the stage request published")
	}
	if intent.PluginsEnabled == nil {
		return errors.New("the recorded destination names no plugin choice the stage request published")
	}
	qualifiedArtifact, err := freshstrategy.QualifiedUpgradeAdoptionArtifactID()
	if err != nil {
		return fmt.Errorf("this build carries no accepted CPA adoption artifact to compare the recorded destination against: %w", err)
	}
	if intent.ExpectedActiveArtifactID != qualifiedArtifact {
		return errors.New("the recorded destination was staged on an artifact this build's adoption record is not qualified against")
	}
	return nil
}

// managedDestinationSourceClass is the source class every Managed destination
// stage publishes. The import copies a 1.x stack's own CPA state, and the class is
// what makes the Supervisor apply that arm; a row naming another one was written
// by a request this step never submitted.
const managedDestinationSourceClass = "legacy_managed"

// validManagedSourceIdentity accepts the identity the managed source stamps and
// the Runtime Supervisor validates for `legacy_managed`: the `external:sha256:`
// prefix and a lowercase SHA-256 digest. A destination staged for anything else
// was staged for another source.
func validManagedSourceIdentity(value string) bool {
	digest, ok := strings.CutPrefix(strings.TrimSpace(value), "external:sha256:")
	return ok && identity.IsValidSHA256Hex(digest)
}

// validManagedServerFingerprint accepts exactly the shape
// freshkeyapply.ExternalServerFingerprint produces: the shared identity prefix and
// a lowercase SHA-256 digest.
func validManagedServerFingerprint(value string) bool {
	digest, ok := strings.CutPrefix(strings.TrimSpace(value), "external:sha256:")
	return ok && identity.IsValidSHA256Hex(digest)
}

// proveVerifyExternalTarget proves the live External target with the Manager's
// own stored credential.
//
// Stored-key equality says the Manager holds the secret the connect step proved;
// it does not say the CPA still accepts it, nor that the host behind the recorded
// address is still the server that was proved. So the gate repeats the connect
// step's four reads with that stored credential: the version, the v8
// configuration, the authenticated client-key list and the gateway model surface.
// A CPA that rotated its key, a host that changed behind the address, or a
// management contract that no longer answers is a refusal here — before the
// verdict is recorded, because a completion is exactly the claim that a restarted
// Manager can administer this target.
func proveVerifyExternalTarget(ctx context.Context, options VerifyOptions, target verifyTarget, binding upgradeoperation.ExternalBinding) error {
	managementKey := strings.TrimSpace(target.connection.ManagementKey)
	probes := ExternalConnectOptions{
		BaseURL:                binding.BaseURL,
		ManagementKey:          managementKey,
		observeExternalVersion: options.observeExternalVersion,
		readExternalKeys:       options.readExternalKeys,
		proveModelSurface:      options.proveModelSurface,
		readV8Config:           options.readV8Config,
	}
	report, err := externalConnectVersion(ctx, probes, binding.BaseURL, managementKey)
	if err != nil {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the management secret the Manager stores did not authenticate against the CPA the External connect step recorded, so a restarted Manager could not administer it; nothing was written", managementReadFailureCause(err))
	}
	if report.WriteBlocked {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the CPA the External connect step recorded no longer serves the v8 management contract this Manager writes, so a restarted Manager could not administer it; nothing was written", nil)
	}
	if err := externalConnectConfig(ctx, probes, binding.BaseURL, managementKey); err != nil {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the CPA the External connect step recorded did not answer the authenticated configuration read with the management secret the Manager stores, so a restarted Manager could not administer it; nothing was written", managementReadFailureCause(err))
	}
	snapshot, err := externalConnectKeys(ctx, probes, binding.BaseURL, managementKey)
	if err != nil {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the CPA the External connect step recorded did not answer the authenticated client-key read with the management secret the Manager stores, so a restarted Manager could not administer it; nothing was written", managementReadFailureCause(err))
	}
	clientKey := ""
	if len(snapshot.Keys) > 0 {
		clientKey = snapshot.Keys[0]
	}
	if err := externalConnectModelSurface(ctx, probes, binding.BaseURL, clientKey); err != nil {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the gateway behind the CPA the External connect step recorded did not answer its model surface, so the target a restarted Manager would administer is not serving; nothing was written", modelSurfaceFailureCause(err))
	}
	if freshkeyapply.ExternalServerFingerprint(report.Version, snapshot.SourceSHA256) != binding.ServerFingerprint {
		return newUpgradeError(UpgradeErrorVerifyBindingDrifted,
			"the CPA answering the recorded address no longer matches the server and configuration fingerprint the External connect step proved; run upgrade external-connect --reprove if its key or configuration changed intentionally, and nothing was written", nil)
	}
	return nil
}

// resolveManagedVerifyRuntime returns the Supervisor client the Managed gate
// observes.
//
// A caller that already holds the Manager's Runtime client supplies it. Everything
// else, including the command line and a handler built without one, falls back to
// the Manager's own configuration, which is the same client the import stages
// through: a Manager not configured for the Manager-owned Runtime has no
// Supervisor to report the destination, and the gate refuses instead of guessing
// which CPA the operation ran.
func resolveManagedVerifyRuntime(runtime VerifyRuntime) (VerifyRuntime, error) {
	if runtime != nil {
		return runtime, nil
	}
	cfg, err := config.LoadWithoutCreatingDefault()
	if err != nil {
		return nil, fmt.Errorf("load the Manager configuration: %w", err)
	}
	if !cfg.EmbeddedRuntimeConfigured() {
		return nil, errors.New("this Manager is not configured for the Manager-owned Runtime (CPAMP_RUNTIME_URL and CPAMP_RUNTIME_TOKEN_FILE), so it cannot report the destination CPA the CPA import staged")
	}
	return runtimeservice.NewEmbeddedClientWithTokenSource(
		cfg.RuntimeURL,
		runtimeservice.NewFileRuntimeTokenSource(cfg.RuntimeTokenFile),
	), nil
}

// proveManagedVerifyRuntimeAuthority proves, against the live Supervisor, that
// the destination this gate would complete is the one the record describes.
//
// The recorded attempt names the Runtime identity and the artifact its stage
// request carried; the Supervisor names the identity it runs under and the
// artifact it is executing now. A Supervisor that restarted under an artifact
// this build's adoption record never accepted, or that is running another
// identity, answers the destination's four management reads exactly as the
// recorded one did — the URL, the management secret and the client key list are
// unchanged — so the reads below cannot tell them apart. This comparison is
// therefore the only proof that the CPA the Manager will administer after the
// upgrade is the one the import staged, and it runs before any of those reads.
// The artifact this build accepts is supplied by the caller, which derives it from
// the compiled adoption authority the import proved.
func proveManagedVerifyRuntimeAuthority(ctx context.Context, runtimeClient VerifyRuntime, qualifiedArtifact model.RuntimeArtifactID, destination cpaImportDestinationRecord) error {
	unproven := func(reason string, cause error) error {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven, reason, cause)
	}
	if runtimeClient == nil {
		return unproven("the Manager-owned Runtime is not configured, so the active gateway artifact of the Managed destination cannot be confirmed; nothing was written", nil)
	}
	status, err := runtimeClient.Status(ctx)
	if err != nil {
		return unproven("the Manager-owned Runtime did not answer, so the active gateway artifact of the Managed destination cannot be confirmed; nothing was written", err)
	}
	if err := status.Validate(); err != nil {
		return unproven("the Manager-owned Runtime reported a status this build cannot read, so the active gateway artifact of the Managed destination cannot be confirmed; nothing was written", err)
	}
	if status.ProtocolVersion != "v1" || status.ActiveGatewayArtifact == nil {
		return unproven("the Manager-owned Runtime did not report an active gateway artifact, so the destination CPA this operation imported into cannot be confirmed; nothing was written", nil)
	}
	// The Supervisor reports its observed artifact in every state, including
	// offline and starting, so an artifact match alone does not prove it is
	// running the destination CPA now. Only a Ready Supervisor is administering
	// the CPA that answers the probes below.
	if reason := managedRuntimeReadyFailure(status); reason != "" {
		return unproven(reason+"; nothing was written", nil)
	}
	// The running artifact has to be the one this build's authority accepted and
	// the one the stage request was published against. Either difference means the
	// destination the Supervisor runs is not the destination this operation
	// staged, even when the CPA at the recorded address answers.
	if running := status.ActiveGatewayArtifact.ArtifactID; running != qualifiedArtifact {
		return unproven(fmt.Sprintf(
			"the Manager-owned Runtime is running gateway artifact %q, and this build's accepted CPA adoption record is for %q, so the Managed destination cannot be confirmed; nothing was written",
			running, qualifiedArtifact), nil)
	}
	if staged := destination.Intent.ExpectedActiveArtifactID; staged != status.ActiveGatewayArtifact.ArtifactID {
		return unproven(fmt.Sprintf(
			"the CPA import staged this destination on gateway artifact %q, and the Manager-owned Runtime is running %q, so the destination this operation wrote is not the one now serving; nothing was written",
			staged, status.ActiveGatewayArtifact.ArtifactID), nil)
	}
	// The identity is the Supervisor's authority over its own destination: a
	// different one never staged this request, and the secret the Manager stores
	// was minted under the recorded one. The recorded identity is never empty — the
	// attempt evidence refuses a row without one — so the comparison is exact.
	if staged, running := destination.Intent.ExpectedRuntimeIdentity, status.Identity; staged != running {
		return unproven(fmt.Sprintf(
			"the CPA import staged this destination on Runtime %q, and the Supervisor running now reports %q, so the recorded destination belongs to an authority this Manager cannot administer; nothing was written",
			staged, running), nil)
	}
	return nil
}

// managedRuntimeReadyFailure names why a Supervisor status does not prove that a
// ready destination CPA is running, and reports "" when it does.
//
// The Supervisor reports its observed artifact in every state — offline and
// starting included — so an artifact, identity and URL that all match still say
// nothing about whether the destination CPA answers now. Both steps that address
// the destination on the strength of that status, the completion gate and a replay
// of a recorded import, refuse the same states with this one reason; they differ
// only in the error code they type it with and in what they say about writes.
func managedRuntimeReadyFailure(status model.RuntimeObservedStatus) string {
	if status.State == model.RuntimeStateReady {
		return ""
	}
	return fmt.Sprintf(
		"the Manager-owned Runtime reported state %q rather than %q, so the Supervisor is not running a ready Managed destination CPA",
		status.State, model.RuntimeStateReady)
}

// proveVerifyManagedTarget proves the live Managed destination with the Manager's
// own stored credential.
//
// The import handed the destination a secret only this Manager holds, and recorded
// the server facts it observed. Three things can still be wrong at completion time:
// the stored connection may not be the sealed credential the Manager would
// actually present after a restart, the Supervisor may be running an artifact or
// identity the stage request was never published under, and the CPA answering the
// recorded address may no longer be the server the import proved. So this gate
// asks the live Supervisor for its artifact and identity and compares them with
// the recorded attempt, re-reads the sealed connection with the Manager data key,
// requires the URL both records name to be the same endpoint, and repeats the four
// v8 management reads — version, configuration document, authenticated client-key
// list, gateway model surface — with that credential. The fingerprint those reads
// produce must equal the one the import recorded.
func proveVerifyManagedTarget(ctx context.Context, options VerifyOptions, db *sql.DB, target verifyTarget) error {
	destination := target.attempt
	managementKey := strings.TrimSpace(target.connection.ManagementKey)
	if !cpa.ValidV8ManagementKey(managementKey) {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the Manager's stored CPA connection carries no usable management secret, so the destination the CPA import handed over could not be administered by a restarted Manager; nothing was written", nil)
	}
	// The two stored secrets must be the same one: the Manager presents the
	// connection it resolved, and the destination accepts the secret the import
	// staged. Comparing them here is what makes the four reads below a proof about
	// this Manager rather than about any credential that happens to answer.
	if !security.EqualHMAC(managementKey, strings.TrimSpace(target.attemptSecret)) {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the Manager's stored CPA connection does not carry the management secret the CPA import recorded for this destination, so a restarted Manager could not administer the CPA this run would verify; nothing was written", nil)
	}
	if err := verifyStoredManagedConnection(ctx, db, target); err != nil {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the Manager's stored CPA connection is not the sealed credential the CPA import handed the destination, so a restarted Manager could not administer the CPA this run would verify; nothing was written", err)
	}
	// What the Manager administers after the upgrade is the CPA the Supervisor
	// started, not merely whatever answers the recorded address. The Supervisor's
	// own report is the only statement about that, so it is read before the four
	// management reads below: an artifact or identity that is not the recorded one
	// would answer every one of them exactly as the staged destination did.
	//
	// The artifact this build accepts is derived first: a build whose compiled
	// record authorizes no staging transition has nothing to compare the
	// Supervisor's report with.
	if _, err := qualifiedUpgradeAdoption(); err != nil {
		return err
	}
	qualifiedArtifact, err := freshstrategy.QualifiedUpgradeAdoptionArtifactID()
	if err != nil {
		return err
	}
	runtimeClient, err := resolveManagedVerifyRuntime(options.Runtime)
	if err != nil {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the Manager-owned Runtime is not configured or reachable, so the active gateway artifact of the Managed destination cannot be confirmed; nothing was written", err)
	}
	if err := proveManagedVerifyRuntimeAuthority(ctx, runtimeClient, qualifiedArtifact, destination); err != nil {
		return err
	}
	probes := ExternalConnectOptions{
		BaseURL:                destination.BaseURL,
		ManagementKey:          managementKey,
		observeExternalVersion: options.observeExternalVersion,
		readExternalKeys:       options.readExternalKeys,
		proveModelSurface:      options.proveModelSurface,
		readV8Config:           options.readV8Config,
	}
	report, err := externalConnectVersion(ctx, probes, destination.BaseURL, managementKey)
	if err != nil {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the management secret the Manager stores did not authenticate against the destination the CPA import recorded, so a restarted Manager could not administer it; nothing was written", managementReadFailureCause(err))
	}
	if report.WriteBlocked {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the destination the CPA import recorded no longer serves the v8 management contract this Manager writes, so a restarted Manager could not administer it; nothing was written", nil)
	}
	if err := externalConnectConfig(ctx, probes, destination.BaseURL, managementKey); err != nil {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the destination the CPA import recorded did not answer the authenticated configuration read with the management secret the Manager stores, so a restarted Manager could not administer it; nothing was written", managementReadFailureCause(err))
	}
	snapshot, err := externalConnectKeys(ctx, probes, destination.BaseURL, managementKey)
	if err != nil {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the destination the CPA import recorded did not answer the authenticated client-key read with the management secret the Manager stores, so a restarted Manager could not administer it; nothing was written", managementReadFailureCause(err))
	}
	clientKey := ""
	if len(snapshot.Keys) > 0 {
		clientKey = snapshot.Keys[0]
	}
	if err := managedConnectModelSurface(ctx, probes, destination.BaseURL, clientKey); err != nil {
		return newUpgradeError(UpgradeErrorVerifyConnectionUnproven,
			"the gateway behind the destination the CPA import recorded did not answer its model surface, so the target a restarted Manager would administer is not serving; nothing was written", modelSurfaceFailureCause(err))
	}
	if freshkeyapply.ExternalServerFingerprint(report.Version, snapshot.SourceSHA256) != destination.ServerFingerprint {
		return newUpgradeError(UpgradeErrorVerifyBindingDrifted,
			"the CPA answering the recorded destination address no longer matches the server fingerprint the CPA import proved; re-run the import against the destination this Manager staged if its build or configuration changed intentionally, and nothing was written", nil)
	}
	return nil
}

// verifyStoredManagedConnection proves the Manager's own CPA connection is the
// sealed credential the Managed destination was handed.
//
// A stored connection that happens to hold the same plaintext proves nothing on
// its own: the value the next start reads is the envelope on disk, and a
// plaintext secret left there by an older build is not a sealed credential this
// Manager can re-read with its data key. So the raw settings row is checked for a
// valid envelope, decrypted with the existing data key, and required to equal the
// connection the store resolved — and to name the same endpoint as the recorded
// destination.
func verifyStoredManagedConnection(ctx context.Context, db *sql.DB, target verifyTarget) error {
	if !target.attemptFound {
		return errors.New("the CPA import recorded no destination")
	}
	if target.protector == nil {
		return errors.New("the Manager data key is unavailable")
	}
	sealed, err := readSealedManagerConnectionSecret(ctx, db)
	if err != nil {
		return err
	}
	if !security.IsValidProtectedEnvelope(sealed) {
		return errors.New("the Manager's stored CPA connection carries no sealed management secret")
	}
	secret, err := target.protector.UnprotectString(sealed)
	if err != nil {
		return fmt.Errorf("decrypt the Manager's sealed management secret: %w", err)
	}
	secret = strings.TrimSpace(secret)
	if !cpa.ValidV8ManagementKey(secret) {
		return errors.New("the Manager's sealed management secret is not a usable v8 key")
	}
	if !security.EqualHMAC(strings.TrimSpace(target.connection.ManagementKey), secret) {
		return errors.New("the sealed management secret is not the connection the Manager resolved")
	}
	connectionAddress, err := canonicalVerifyAddress(target.connection.BaseURL)
	if err != nil || connectionAddress == "" {
		return errors.New("the Manager's stored CPA connection names no usable address")
	}
	destinationAddress, err := canonicalVerifyAddress(target.attempt.BaseURL)
	if err != nil {
		return fmt.Errorf("the recorded destination names an address that is not a usable endpoint: %w", err)
	}
	if connectionAddress != destinationAddress {
		return errors.New("the Manager's stored CPA connection names a different address than the recorded destination")
	}
	return nil
}

// readSealedManagerConnectionSecret reads the sealed management key exactly as it
// is stored on disk, without decrypting it. The caller decides whether the
// envelope is one this Manager can read.
func readSealedManagerConnectionSecret(ctx context.Context, db *sql.DB) (string, error) {
	var raw string
	err := db.QueryRowContext(ctx, `select value from settings where key = 'manager_config_v1'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("the Manager stores no CPA connection")
	}
	if err != nil {
		return "", fmt.Errorf("read the Manager's stored CPA connection: %w", err)
	}
	var stored model.ManagerConfig
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return "", fmt.Errorf("decode the Manager's stored CPA connection: %w", err)
	}
	return strings.TrimSpace(stored.CPAConnection.ManagementKey), nil
}

// canonicalVerifyAddress applies exactly the two functions the External connect
// step applies to a target address, so the comparison is between spellings of the
// same endpoint and not between an operator's spelling and a stored one.
func canonicalVerifyAddress(address string) (string, error) {
	normalized, err := cpa.NormalizeV8BaseURL(strings.TrimSpace(address))
	if err != nil {
		return "", err
	}
	return identity.CanonicalExternalURL(normalized)
}

// verifyReconciliation re-derives the reconcile step's acceptance decision from
// the evidence the row persisted, and names the operation domains it established.
//
// The decision is the reconcile step's own contract, applied to stored input:
// an acknowledgement accepts a day that can never be completed again, never a
// domain the store could not show, and it never waives lost data. Re-deriving it
// here is what makes a tampered or foreign evidence body a refusal instead of a
// pass, and the coverage marks are checked on disk so accepting a day cannot
// drop the mark that records it.
func verifyReconciliation(ctx context.Context, tx upgradeoperation.Tx, operation model.UpgradeOperation, fingerprint string) (ReconcileEvidence, []string, error) {
	var evidence ReconcileEvidence
	if len(operation.Reconciliation) == 0 {
		return evidence, nil, newUpgradeError(UpgradeErrorVerifyReconciliationInvalid,
			"the recorded operation carries no reconciliation, so nothing proves the copied store holds the source's usage; nothing was written", nil)
	}
	if err := json.Unmarshal(operation.Reconciliation, &evidence); err != nil {
		return evidence, nil, newUpgradeError(UpgradeErrorVerifyReconciliationInvalid,
			"the recorded reconciliation is not a comparison this build can read, so nothing proves the copied store holds the source's usage; nothing was written", err)
	}
	if len(evidence.Domains) == 0 {
		return evidence, nil, newUpgradeError(UpgradeErrorVerifyReconciliationInvalid,
			"the recorded reconciliation compared no domain, so it establishes nothing to verify; nothing was written", nil)
	}
	if digest := strings.TrimSpace(evidence.SourceFingerprint); digest == "" {
		return evidence, nil, newUpgradeError(UpgradeErrorVerifyReconciliationInvalid,
			"the recorded reconciliation names no source fingerprint, so it cannot be tied to a volume; nothing was written", nil)
	} else if digest != fingerprint {
		return evidence, nil, newUpgradeError(UpgradeErrorVerifySourceMismatch,
			"the mounted 1.x source is not the volume the recorded reconciliation compared; nothing was written", nil)
	}
	for _, domain := range evidence.Domains {
		if !domain.Applicable {
			// A domain that did not apply is honest evidence only when it says
			// why: a blank one is a comparison nobody can re-check.
			if strings.TrimSpace(domain.Reason) == "" {
				return evidence, nil, newUpgradeError(UpgradeErrorVerifyReconciliationInvalid, fmt.Sprintf(
					"the recorded reconciliation reports the %q domain as not applicable without saying why; nothing was written", domain.Domain), nil)
			}
			continue
		}
		if err := domain.targetIncompleteError(); err != nil {
			return evidence, nil, newUpgradeError(UpgradeErrorVerifyReconciliationUnacknowledged, fmt.Sprintf(
				"the recorded reconciliation holds a domain the v2 store could not show: %v; nothing was written", err), err)
		}
	}
	established := establishedDomains(evidence.Domains)
	if len(established) == 0 {
		return evidence, nil, newUpgradeError(UpgradeErrorVerifyReconciliationInvalid,
			"the recorded reconciliation established no operation domain, so it proves no copied data; nothing was written", nil)
	}
	if len(evidence.UnacknowledgedDays) > 0 {
		return evidence, nil, newUpgradeError(UpgradeErrorVerifyReconciliationUnacknowledged, fmt.Sprintf(
			"the recorded reconciliation left %s undecided: %s; acknowledge them and run the reconciliation again, and nothing was written",
			plural(len(evidence.UnacknowledgedDays), "day", "days"), strings.Join(evidence.UnacknowledgedDays, ", ")), nil)
	}
	acknowledged := make(map[string]bool, len(evidence.AcknowledgedDays))
	for _, day := range evidence.AcknowledgedDays {
		acknowledged[day] = true
	}
	for _, day := range unionDays(evidence.MismatchDays, evidence.ParityGapDays) {
		if !acknowledged[day] {
			return evidence, nil, newUpgradeError(UpgradeErrorVerifyReconciliationUnacknowledged, fmt.Sprintf(
				"the recorded reconciliation reports %s without an acknowledgement, so it was never accepted; nothing was written", day), nil)
		}
	}
	if len(evidence.IncompleteDays) != len(evidence.AcknowledgedDays) {
		return evidence, nil, newUpgradeError(UpgradeErrorVerifyReconciliationUnacknowledged,
			"the recorded reconciliation accepted days without recording which of them are permanently incomplete, so its marks cannot be re-checked; nothing was written", nil)
	}
	marked := make(map[string]bool, len(evidence.IncompleteDays))
	for _, day := range evidence.IncompleteDays {
		if !isReconcileDay(day.Day) || !markedReason(day.Reason) {
			return evidence, nil, newUpgradeError(UpgradeErrorVerifyReconciliationInvalid,
				"the recorded reconciliation names a permanently incomplete day without a day and a reason this build can re-check; nothing was written", nil)
		}
		marked[day.Day] = true
		// The mark is the durable half of the acknowledgement. Accepting the day
		// while the store lost its mark would present a covered number as exact.
		var stored int
		if err := tx.QueryRowContext(ctx,
			`select count(*) from usage_coverage_incomplete_days where day = ? and reason <> ''`, day.Day).Scan(&stored); err != nil {
			return evidence, nil, fmt.Errorf("read the recorded coverage mark for %s: %w", day.Day, err)
		}
		if stored == 0 {
			return evidence, nil, newUpgradeError(UpgradeErrorVerifyReconciliationUnacknowledged, fmt.Sprintf(
				"the recorded reconciliation accepted %s as permanently incomplete but the store holds no such coverage mark; nothing was written", day.Day), nil)
		}
	}
	for _, day := range evidence.AcknowledgedDays {
		if !marked[day] {
			return evidence, nil, newUpgradeError(UpgradeErrorVerifyReconciliationUnacknowledged, fmt.Sprintf(
				"the recorded reconciliation accepted %s without recording it as permanently incomplete; nothing was written", day), nil)
		}
	}
	return evidence, established, nil
}

// isReconcileDay reports whether a value is a UTC day the reconcile step writes.
func isReconcileDay(value string) bool {
	if len(value) != len("2006-01-02") || value[4] != '-' || value[7] != '-' {
		return false
	}
	_, err := time.Parse("2006-01-02", value)
	return err == nil
}

// markedReason reports whether a recorded mark carries one of the reasons the
// reconcile step can write.
func markedReason(reason string) bool {
	switch reason {
	case coverageincomplete.ReasonProjectionLostBeforeV1146, coverageincomplete.ReasonReconcileMismatch, coverageincomplete.ReasonDerivedRollupLost:
		return true
	}
	return false
}

// verifyRequiredDomains is the applicable domain set of this operation: the
// copy-in's own domain, every domain the reconciliation established, and the
// target step's domains. `cpa_config` is required for both modes — the External
// connect step and the Managed CPA import each record it — while `identity` is
// the External connect step's own observation and has no Managed producer. It is
// derived and never the whole model vocabulary: `usage_events` is a domain no
// step of this build records, and requiring it would make every real operation
// unverifiable.
func verifyRequiredDomains(mode string, established []string) []string {
	required := make([]string, 0, len(established)+3)
	required = append(required, model.UpgradeDomainSourceCopy)
	for _, domain := range established {
		if !containsVerifyDomain(required, domain) {
			required = append(required, domain)
		}
	}
	if mode == model.UpgradeTargetExternal && !containsVerifyDomain(required, model.UpgradeDomainIdentity) {
		required = append(required, model.UpgradeDomainIdentity)
	}
	if !containsVerifyDomain(required, model.UpgradeDomainCPAConfig) {
		required = append(required, model.UpgradeDomainCPAConfig)
	}
	return required
}

func containsVerifyDomain(values []string, domain string) bool {
	for _, value := range values {
		if value == domain {
			return true
		}
	}
	return false
}

// requireVerifyDomains proves every applicable domain is recorded. A domain the
// source could not provide is not in the required set at all, so this never asks
// for work no step could perform.
func requireVerifyDomains(operation model.UpgradeOperation, required []string) error {
	missing := make([]string, 0, len(required))
	for _, domain := range required {
		if !operation.HasDomain(domain) {
			missing = append(missing, domain)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return newUpgradeError(UpgradeErrorVerifyDomainsIncomplete, fmt.Sprintf(
		"the recorded operation does not hold %s this target needs, so the copy is not proven for them; nothing was written: %s",
		plural(len(missing), "domain", "domains"), strings.Join(missing, ", ")), nil)
}

// acquireVerifySourceFence takes the stopped source's ownership marker through a
// read-only descriptor. A live writer is a refusal (the source was not stopped),
// and a root with no marker is a source this step cannot prove anything about.
func acquireVerifySourceFence(sourceRoot string) (*processlock.Lock, error) {
	databasePath := SourceDatabasePath(sourceRoot)
	if _, err := os.Stat(databasePath); err != nil {
		return nil, newUpgradeError(UpgradeErrorVerifySourceUnavailable,
			"the mounted 1.x directory holds no source database, so the volume the operation copied cannot be observed; nothing was written", err)
	}
	lock, err := processlock.AcquireExistingReadOnly(databasePath)
	if err == nil {
		return lock, nil
	}
	if errors.Is(err, processlock.ErrLocked) {
		return nil, newUpgradeError(UpgradeErrorCode(CodeSourceBusy),
			"the 1.x source is held by a live writer, so it was not observed and nothing was written; stop the source and run the verification again", err)
	}
	return nil, newUpgradeError(UpgradeErrorVerifySourceUnavailable,
		"the 1.x source ownership marker could not be taken, so the volume cannot be proven stopped; nothing was written", err)
}

// acquireVerifyTargetFence takes the v2 data root's ownership lock, the same
// marker a running Manager holds. A held lock is a typed refusal: another writer
// serving this store must not share it with a completion verdict.
func acquireVerifyTargetFence(databasePath string) (*processlock.Lock, error) {
	lock, err := processlock.Acquire(databasePath)
	if err == nil {
		return lock, nil
	}
	if errors.Is(err, processlock.ErrLocked) {
		return nil, newUpgradeError(UpgradeErrorVerifyV2Running,
			"the v2 data root is owned by another writer, so this run did not verify or record anything; stop the process that owns it and run the verification again", nil)
	}
	return nil, fmt.Errorf("take the v2 data root ownership lock: %w", err)
}

// mapVerifyRecordError turns the operation row's own refusal into the typed
// refusal of the step. A row that moved while the gate decided is a conflict, a
// frozen row means another run already recorded the verdict, and anything else
// is reported as the record failure it is.
func mapVerifyRecordError(err error) error {
	switch {
	case errors.Is(err, upgradeoperation.ErrStageRegression):
		return newUpgradeError(UpgradeErrorVerifyNotApplicable,
			"the operation moved past the stage this run could record while it was verifying, so the verdict was not recorded; nothing was written", err)
	case errors.Is(err, upgradeoperation.ErrConflict):
		return newUpgradeError(UpgradeErrorVerifyConflict,
			"the recorded operation changed while it was being verified, so the verdict this run proved was about a row that no longer exists; nothing was recorded, and running the verification again re-proves the current one", err)
	default:
		return fmt.Errorf("record the verification: %w", err)
	}
}
