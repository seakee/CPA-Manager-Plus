package upgrade

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

type rowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type durableReconcileCodes struct {
	invalid     UpgradeErrorCode
	unacked     UpgradeErrorCode
	fingerprint UpgradeErrorCode
}

// RequireReconcileStage enforces the shared stage admissibility contract for
// entering the reconciliation step across CLI and worker execution paths.
func RequireReconcileStage(recorded model.UpgradeOperation, found bool) error {
	if !found {
		return newUpgradeError(UpgradeErrorReconcileNotCopiedIn,
			"the v2 data root holds no recorded copy-in, so there is nothing to reconcile against; nothing was compared and nothing was marked", nil)
	}
	if recorded.Stage != "" && (!recorded.Stage.CanAdvanceTo(model.UpgradeStageReconcile) ||
		(recorded.Stage == model.UpgradeStageFailed && !recorded.CanLeaveFailedTo(model.UpgradeStageReconcile))) {
		return newUpgradeError(UpgradeErrorReconcileStageClosed, fmt.Sprintf(
			"the recorded operation is at stage %q, which cannot enter the reconcile step; nothing was compared and nothing was marked",
			recorded.Stage), nil)
	}
	return nil
}

// RequirePendingReconcileClear fails closed with UpgradeErrorReconcileMismatch
// whenever persisted or observed reconciliation UTC dates remain unacknowledged.
func RequirePendingReconcileClear(recorded model.UpgradeOperation, stepName string) error {
	if len(recorded.PendingReconcileDays) == 0 {
		return nil
	}
	return newUpgradeError(UpgradeErrorReconcileMismatch, fmt.Sprintf(
		"reconciliation dates remain unacknowledged before %s (%s); re-run cpa-manager-plus upgrade reconcile with explicit acknowledgment, and nothing was written",
		stepName, strings.Join(recorded.PendingReconcileDays, ", ")), nil)
}

// RequireCPAImportStage enforces the shared stage and durable-reconciliation
// contract before Managed CPA import can stage, probe, or record any state.
func RequireCPAImportStage(ctx context.Context, q rowQueryer, recorded model.UpgradeOperation, found bool) error {
	if !found {
		return newUpgradeError(UpgradeErrorCPAStageConflict,
			"the v2 data root holds no recorded copy-in; run cpa-manager-plus upgrade run and reconcile before importing the CPA, and nothing was written", nil)
	}
	switch recorded.Stage {
	case model.UpgradeStageReconcile, model.UpgradeStageCPAImport:
	case model.UpgradeStageFailed:
		switch recorded.LastForwardStage {
		case model.UpgradeStageReconcile, model.UpgradeStageCPAImport:
		case "", model.UpgradeStagePreflight, model.UpgradeStageCopyIn:
			return newUpgradeError(UpgradeErrorCPAStageConflict,
				"the upgrade failed before reconciliation completed; run cpa-manager-plus upgrade reconcile first, and nothing was written", nil)
		default:
			return newUpgradeError(UpgradeErrorCPAStageConflict, fmt.Sprintf(
				"the upgrade failed after reaching %q and cannot resume at %s; nothing was written",
				recorded.LastForwardStage, model.UpgradeStageCPAImport), nil)
		}
	case model.UpgradeStageCopyIn:
		return newUpgradeError(UpgradeErrorCPAStageConflict,
			"the upgrade is still at stage copy_in and has not been reconciled; run cpa-manager-plus upgrade reconcile first, and nothing was written", nil)
	case model.UpgradeStageExternalConnect:
		return newUpgradeError(UpgradeErrorCPAStageConflict,
			"external_connect has already bound this upgrade to an External CPA; Managed CPA import and External CPA connect are mutually exclusive, and nothing was written", nil)
	default:
		return newUpgradeError(UpgradeErrorCPAStageConflict, fmt.Sprintf(
			"the upgrade is at stage %q and cannot enter %s; nothing was written",
			recorded.Stage, model.UpgradeStageCPAImport), nil)
	}
	if q != nil {
		if hasExternalBinding, err := hasRecordedExternalBindingRow(ctx, q); err != nil {
			return fmt.Errorf("inspect recorded External CPA binding: %w", err)
		} else if hasExternalBinding {
			return newUpgradeError(UpgradeErrorCPAStageConflict,
				"the v2 store already records an External CPA binding; Managed CPA import and External CPA connect are mutually exclusive, and nothing was written", nil)
		}
	}
	if err := RequirePendingReconcileClear(recorded, "cpa-import"); err != nil {
		return err
	}
	_, established, err := validateDurableReconciliationFrom(ctx, q, recorded, strings.TrimSpace(recorded.SourceFingerprintAfter), durableReconcileCodes{
		invalid:     UpgradeErrorCPAStageConflict,
		unacked:     UpgradeErrorReconcileMismatch,
		fingerprint: UpgradeErrorCPAStageConflict,
	})
	if err != nil {
		return err
	}
	for _, domain := range established {
		if !recorded.HasDomain(domain) {
			return newUpgradeError(UpgradeErrorCPAStageConflict, fmt.Sprintf(
				"the recorded operation is missing reconciled domain %q; run cpa-manager-plus upgrade reconcile first, and nothing was written", domain), nil)
		}
	}
	return nil
}

// RequireExternalConnectStage enforces the shared stage, mutual-exclusion, and
// durable-reconciliation contract before External CPA connect can probe or
// mutate any v2 store state.
func RequireExternalConnectStage(ctx context.Context, q rowQueryer, recorded model.UpgradeOperation, found bool) error {
	if !found {
		return newUpgradeError(UpgradeErrorExternalNotApplicable,
			"the v2 data root holds no recorded copy-in, so there is no operation for the External connect step to continue; nothing was written", nil)
	}
	switch recorded.Stage {
	case model.UpgradeStageReconcile, model.UpgradeStageExternalConnect:
	case model.UpgradeStageFailed:
		switch recorded.LastForwardStage {
		case model.UpgradeStageReconcile, model.UpgradeStageExternalConnect:
		case "", model.UpgradeStagePreflight, model.UpgradeStageCopyIn:
			return newUpgradeError(UpgradeErrorExternalNotApplicable,
				"the recorded operation has not completed reconciliation yet; run cpa-manager-plus upgrade reconcile first, and nothing was written", nil)
		default:
			return newUpgradeError(UpgradeErrorExternalNotApplicable,
				fmt.Sprintf("the recorded operation failed after reaching %q, which cannot enter the External connect step; nothing was written", recorded.LastForwardStage), nil)
		}
	case model.UpgradeStageCopyIn:
		return newUpgradeError(UpgradeErrorExternalNotApplicable,
			"the recorded operation is still at stage copy_in and has not completed reconciliation; run cpa-manager-plus upgrade reconcile first, and nothing was written", nil)
	case model.UpgradeStageCPAImport:
		return newUpgradeError(UpgradeErrorExternalNotApplicable,
			"the recorded operation is at stage cpa_import; Managed CPA import and External CPA connect are mutually exclusive, and nothing was written", nil)
	default:
		return newUpgradeError(UpgradeErrorExternalNotApplicable,
			fmt.Sprintf("the recorded operation is at stage %q, which cannot enter the External connect step; nothing was written", recorded.Stage), nil)
	}
	if q != nil {
		if hasManagedAttempt, err := hasRecordedManagedDestinationAttempt(ctx, q); err != nil {
			return fmt.Errorf("inspect Managed CPA destination staging record: %w", err)
		} else if hasManagedAttempt {
			return newUpgradeError(UpgradeErrorExternalNotApplicable,
				"the v2 store already records a Managed CPA staging attempt; Managed CPA import and External CPA connect are mutually exclusive, and nothing was written", nil)
		}
	}
	if err := RequirePendingReconcileClear(recorded, "external-connect"); err != nil {
		return err
	}
	_, established, err := validateDurableReconciliationFrom(ctx, q, recorded, strings.TrimSpace(recorded.SourceFingerprintAfter), durableReconcileCodes{
		invalid:     UpgradeErrorExternalNotApplicable,
		unacked:     UpgradeErrorReconcileMismatch,
		fingerprint: UpgradeErrorExternalNotApplicable,
	})
	if err != nil {
		return err
	}
	for _, domain := range established {
		if !recorded.HasDomain(domain) {
			return newUpgradeError(UpgradeErrorExternalNotApplicable, fmt.Sprintf(
				"the recorded operation is missing reconciled domain %q; run cpa-manager-plus upgrade reconcile first, and nothing was written", domain), nil)
		}
	}
	return nil
}

// RequireVerifyStage enforces the shared stage and durable-reconciliation entry
// contract before verification runs.
func RequireVerifyStage(ctx context.Context, q rowQueryer, recorded model.UpgradeOperation, found bool) error {
	mode, err := requireVerifyStageEntry(ctx, q, recorded, found)
	if err != nil {
		return err
	}
	if !found || q == nil {
		return nil
	}
	_, established, err := validateDurableReconciliationFrom(ctx, q, recorded, strings.TrimSpace(recorded.SourceFingerprintAfter), durableReconcileCodes{
		invalid:     UpgradeErrorVerifyReconciliationInvalid,
		unacked:     UpgradeErrorVerifyReconciliationUnacknowledged,
		fingerprint: UpgradeErrorVerifySourceMismatch,
	})
	if err != nil {
		return err
	}
	return requireVerifyDomains(recorded, verifyRequiredDomains(mode, established))
}

func requireVerifyStageEntry(ctx context.Context, q rowQueryer, recorded model.UpgradeOperation, found bool) (string, error) {
	if !found {
		return "", newUpgradeError(UpgradeErrorVerifyNotApplicable,
			"the v2 data root holds no recorded copy-in, so there is no operation to verify; nothing was written", nil)
	}
	if recorded.Stage == model.UpgradeStageComplete {
		if err := RequirePendingReconcileClear(recorded, "verify"); err != nil {
			return "", err
		}
		if q == nil {
			return "", nil
		}
		hasBinding, err := hasRecordedExternalBindingRow(ctx, q)
		if err != nil {
			return "", fmt.Errorf("read the External connect binding: %w", err)
		}
		return verifyTargetMode(recorded, hasBinding)
	}
	if err := verifyEntry(recorded); err != nil {
		return "", err
	}
	if err := RequirePendingReconcileClear(recorded, "verify"); err != nil {
		return "", err
	}
	if q == nil {
		return "", nil
	}
	hasBinding, err := hasRecordedExternalBindingRow(ctx, q)
	if err != nil {
		return "", fmt.Errorf("read the External connect binding: %w", err)
	}
	mode, err := verifyTargetMode(recorded, hasBinding)
	if err != nil {
		return "", err
	}
	if err := requireVerifyEntryForMode(recorded, mode); err != nil {
		return "", err
	}
	return mode, nil
}

// RequireActionStage inspects a read-only snapshot of databasePath and enforces
// the canonical stage and durable-reconciliation gate for action.
func RequireActionStage(ctx context.Context, databasePath, action string, extraPendingDays []string) error {
	return RequireActionStageWithScratch(ctx, databasePath, "", "", action, extraPendingDays)
}

// RequireActionStageWithScratch validates scratchDir against legacyRoot and
// databasePath's parent directory, opens a read-only snapshot under scratchDir,
// and enforces the canonical stage and durable-reconciliation gate for action.
func RequireActionStageWithScratch(ctx context.Context, databasePath, scratchDir, legacyRoot, action string, extraPendingDays []string) error {
	return RequireActionStageWithBackup(ctx, databasePath, scratchDir, legacyRoot, "", action, extraPendingDays)
}

// RequireActionStageWithBackup validates scratchDir against legacyRoot,
// databasePath's parent directory, and non-empty bundleDir, opens a read-only
// snapshot under scratchDir, and enforces the canonical stage and
// durable-reconciliation gate for action.
func RequireActionStageWithBackup(ctx context.Context, databasePath, scratchDir, legacyRoot, bundleDir, action string, extraPendingDays []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := requireRecoveredCopyIn(databasePath); err != nil {
		return err
	}
	if err := ValidateScratchDirWithBackup(scratchDir, legacyRoot, filepath.Dir(databasePath), bundleDir); err != nil {
		return snapshotReadError(ctx, err)
	}
	if action == "reconcile" && strings.TrimSpace(scratchDir) != "" && strings.TrimSpace(legacyRoot) != "" {
		if scratchAbs, err := filepath.Abs(strings.TrimSpace(scratchDir)); err == nil {
			if sourceAbs, srcErr := filepath.Abs(strings.TrimSpace(legacyRoot)); srcErr == nil &&
				within(canonicalPath(scratchAbs), canonicalPath(sourceAbs)) {
				return newUpgradeError(UpgradeErrorSnapshotFailed,
					"the reconciliation scratch area must not contain the 1.x source root", ErrSnapshotFailed)
			}
		}
	}
	var (
		recorded model.UpgradeOperation
		found    bool
		snapshot *Snapshot
	)
	if _, statErr := os.Lstat(databasePath); statErr == nil {
		opened, err := OpenSnapshotWithRetry(ctx, OpenSnapshot, databasePath, strings.TrimSpace(scratchDir))
		if err != nil {
			return snapshotReadError(ctx, err)
		}
		snapshot = opened
		defer func() { _ = snapshot.Close() }()
		op, opFound, err := ReadRecordFrom(ctx, snapshot.DB())
		if err != nil {
			return snapshotReadError(ctx, err)
		}
		if opFound && copyInRecorded(op) {
			recorded = op
			found = true
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	if found && len(extraPendingDays) > 0 {
		recorded.PendingReconcileDays = unionDays(recorded.PendingReconcileDays, extraPendingDays)
	}
	var queryer rowQueryer
	if snapshot != nil {
		queryer = snapshot.DB()
	}
	switch action {
	case "reconcile":
		return RequireReconcileStage(recorded, found)
	case "cpa-import":
		return RequireCPAImportStage(ctx, queryer, recorded, found)
	case "external-connect":
		return RequireExternalConnectStage(ctx, queryer, recorded, found)
	case "verify":
		return RequireVerifyStage(ctx, queryer, recorded, found)
	default:
		return fmt.Errorf("unsupported upgrade stage gate action %q", action)
	}
}

func validateDurableReconciliationFrom(
	ctx context.Context,
	q rowQueryer,
	operation model.UpgradeOperation,
	expectedFingerprint string,
	codes durableReconcileCodes,
) (ReconcileEvidence, []string, error) {
	var evidence ReconcileEvidence
	if strings.TrimSpace(expectedFingerprint) == "" {
		return evidence, nil, newUpgradeError(codes.invalid,
			"the recorded copy-in names no source fingerprint, so the mounted 1.x volume cannot be proven to be the one that was copied; nothing was written", nil)
	}
	if len(operation.Reconciliation) == 0 {
		return evidence, nil, newUpgradeError(codes.invalid,
			"the recorded operation carries no reconciliation, so nothing proves the copied store holds the source's usage; nothing was written", nil)
	}
	if err := json.Unmarshal(operation.Reconciliation, &evidence); err != nil {
		return evidence, nil, newUpgradeError(codes.invalid,
			"the recorded reconciliation is not a comparison this build can read, so nothing proves the copied store holds the source's usage; nothing was written", err)
	}
	if len(evidence.Domains) == 0 {
		return evidence, nil, newUpgradeError(codes.invalid,
			"the recorded reconciliation compared no domain, so it establishes nothing to verify; nothing was written", nil)
	}
	if digest := strings.TrimSpace(evidence.SourceFingerprint); digest == "" {
		return evidence, nil, newUpgradeError(codes.invalid,
			"the recorded reconciliation names no source fingerprint, so it cannot be tied to a volume; nothing was written", nil)
	} else if digest != expectedFingerprint {
		return evidence, nil, newUpgradeError(codes.fingerprint,
			"the mounted 1.x source is not the volume the recorded reconciliation compared; nothing was written", nil)
	}
	for _, domain := range evidence.Domains {
		if !domain.Applicable {
			if strings.TrimSpace(domain.Reason) == "" {
				return evidence, nil, newUpgradeError(codes.invalid, fmt.Sprintf(
					"the recorded reconciliation reports the %q domain as not applicable without saying why; nothing was written", domain.Domain), nil)
			}
			continue
		}
		if err := domain.targetIncompleteError(); err != nil {
			return evidence, nil, newUpgradeError(codes.unacked, fmt.Sprintf(
				"the recorded reconciliation holds a domain the v2 store could not show: %v; nothing was written", err), err)
		}
	}
	established := establishedDomains(evidence.Domains)
	if len(established) == 0 {
		return evidence, nil, newUpgradeError(codes.invalid,
			"the recorded reconciliation established no operation domain, so it proves no copied data; nothing was written", nil)
	}
	if len(evidence.UnacknowledgedDays) > 0 {
		return evidence, nil, newUpgradeError(codes.unacked, fmt.Sprintf(
			"the recorded reconciliation left %s undecided: %s; acknowledge them and run the reconciliation again, and nothing was written",
			plural(len(evidence.UnacknowledgedDays), "day", "days"), strings.Join(evidence.UnacknowledgedDays, ", ")), nil)
	}
	acknowledged := make(map[string]bool, len(evidence.AcknowledgedDays))
	for _, day := range evidence.AcknowledgedDays {
		acknowledged[day] = true
	}
	for _, day := range unionDays(evidence.MismatchDays, evidence.ParityGapDays) {
		if !acknowledged[day] {
			return evidence, nil, newUpgradeError(codes.unacked, fmt.Sprintf(
				"the recorded reconciliation reports %s without an acknowledgement, so it was never accepted; nothing was written", day), nil)
		}
	}
	if len(evidence.IncompleteDays) != len(evidence.AcknowledgedDays) {
		return evidence, nil, newUpgradeError(codes.unacked,
			"the recorded reconciliation accepted days without recording which of them are permanently incomplete, so its marks cannot be re-checked; nothing was written", nil)
	}
	marked := make(map[string]bool, len(evidence.IncompleteDays))
	for _, day := range evidence.IncompleteDays {
		if !isReconcileDay(day.Day) || !markedReason(day.Reason) {
			return evidence, nil, newUpgradeError(codes.invalid,
				"the recorded reconciliation names a permanently incomplete day without a day and a reason this build can re-check; nothing was written", nil)
		}
		marked[day.Day] = true
		if q != nil {
			var tableExists int
			if err := q.QueryRowContext(ctx,
				`select count(*) from sqlite_master where type = 'table' and name = 'usage_coverage_incomplete_days'`).Scan(&tableExists); err != nil {
				return evidence, nil, fmt.Errorf("check coverage mark table for %s: %w", day.Day, err)
			}
			var stored int
			if tableExists > 0 {
				if err := q.QueryRowContext(ctx,
					`select count(*) from usage_coverage_incomplete_days where day = ? and reason <> ''`, day.Day).Scan(&stored); err != nil {
					return evidence, nil, fmt.Errorf("read the recorded coverage mark for %s: %w", day.Day, err)
				}
			}
			if stored == 0 {
				return evidence, nil, newUpgradeError(codes.unacked, fmt.Sprintf(
					"the recorded reconciliation accepted %s as permanently incomplete but the store holds no such coverage mark; nothing was written", day.Day), nil)
			}
		}
	}
	for _, day := range evidence.AcknowledgedDays {
		if !marked[day] {
			return evidence, nil, newUpgradeError(codes.unacked, fmt.Sprintf(
				"the recorded reconciliation accepted %s without recording it as permanently incomplete; nothing was written", day), nil)
		}
	}
	return evidence, established, nil
}

func hasRecordedManagedDestinationAttempt(ctx context.Context, q rowQueryer) (bool, error) {
	var tableExists int
	if err := q.QueryRowContext(ctx,
		`select count(*) from sqlite_master where type = 'table' and name = 'settings'`).Scan(&tableExists); err != nil {
		return false, err
	}
	if tableExists == 0 {
		return false, nil
	}
	var count int
	if err := q.QueryRowContext(ctx, `select count(*) from settings where key = ?`, cpaImportDestinationKeySetting).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func hasRecordedExternalBindingRow(ctx context.Context, q rowQueryer) (bool, error) {
	var tableExists int
	if err := q.QueryRowContext(ctx,
		`select count(*) from sqlite_master where type = 'table' and name = 'upgrade_external_connect'`).Scan(&tableExists); err != nil {
		return false, err
	}
	if tableExists == 0 {
		return false, nil
	}
	var count int
	if err := q.QueryRowContext(ctx, `select count(*) from upgrade_external_connect where id = 1`).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}
