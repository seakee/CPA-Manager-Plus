// Package upgrade is the operator entry point of the v2 Upgrade operation.
//
// "upgrade preflight" inspects a stopped 1.x source and writes nothing, not
// even inside the source root. "upgrade status" reads the single
// upgrade_operations row from a private snapshot of the Manager database.
// Neither subcommand opens a database in place, so both work when the volumes
// are not writable. "upgrade run" performs the backup and copy-in while the 1.x
// server is stopped: it refuses a blocked preflight, writes only inside the v2
// data root and records the outcome. "upgrade reconcile" compares the copied
// store against the stopped source and marks the days that can never be
// completed again; it refuses to mark anything the operator has not
// acknowledged.
package upgrade

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	_ "modernc.org/sqlite"
)

var (
	// ErrUsage marks a caller mistake: bad flags, missing arguments.
	ErrUsage = errors.New("upgrade: usage")
	// ErrBlocked marks a complete but blocked preflight report. The report was
	// already written; only the exit code says the source is not ready.
	ErrBlocked = errors.New("upgrade preflight is blocked")
	// ErrRetryable marks a failure that can succeed unchanged on a later run:
	// the Manager database kept changing while it was being copied. The message
	// carries the shared code (upgrade_source_changed), not a generic usage
	// error, so an operator or a script can tell the two apart.
	ErrRetryable = errors.New("upgrade: retryable")
)

// statusSnapshotAttempts bounds how often `upgrade status` copies a Manager
// database that is being written. A live Manager writes in bursts, so a handful
// of attempts across roughly a second normally finds a quiet window. Preflight
// never retries: a legacy source that is still being written is a stop
// condition, not a race to win.
const (
	statusSnapshotAttempts  = 8
	statusSnapshotBaseDelay = 20 * time.Millisecond
	statusSnapshotMaxDelay  = 200 * time.Millisecond
)

// openSnapshot is a seam so a test can prove the bounded retry and its
// exhaustion without depending on a real race.
var openSnapshot = upgradeservice.OpenSnapshot

// ExitCode maps a Run result to the CLI contract: 0 ready, 1 a refusal the
// operator has to look at — a blocked preflight, a source or v2 data root
// another process holds, an unacknowledged mismatch, a store that cannot show
// what the source holds, a reconciliation that compared nothing, or a store
// whose recorded copy-in does not allow the step — and 2 anything else. A
// refusal is a decision about the migration, not a usage or environment error,
// so every reconcile refusal shares exit 1 and the typed code in the report says
// which one it was. A changed source stays retryable and keeps exit 2: the retry
// marker is what tells the operator to run it again.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, ErrBlocked) {
		return 1
	}
	if code, ok := upgradeservice.UpgradeErrorCodeOf(err); ok && slices.Contains(refusalCodes, code) {
		return 1
	}
	return 2
}

// refusalCodes are the typed failures that are a decision about the migration
// rather than a usage or environment problem: the operator has to look at the
// source, the store, or the day the report names, so the CLI exits 1 and the
// code in the report says which. upgrade_source_changed is deliberately absent:
// a source that moved or is still being written is retryable, so it keeps exit
// 2 like every other retryable failure.
var refusalCodes = []upgradeservice.UpgradeErrorCode{
	upgradeservice.UpgradeErrorCode(upgradeservice.CodeSourceBusy),
	upgradeservice.UpgradeErrorV2Running,
	upgradeservice.UpgradeErrorTargetLocked,
	upgradeservice.UpgradeErrorReconcileMismatch,
	upgradeservice.UpgradeErrorReconcileTargetIncomplete,
	upgradeservice.UpgradeErrorReconcileNothingCompared,
	upgradeservice.UpgradeErrorReconcileNotCopiedIn,
	upgradeservice.UpgradeErrorReconcileStageClosed,
	upgradeservice.UpgradeErrorReconcileRecordIncomplete,
}

func IsHelp(err error) bool { return errors.Is(err, flag.ErrHelp) }

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		writeUsage(stderr)
		return fmt.Errorf("%w: subcommand is required", ErrUsage)
	}
	switch args[0] {
	case "preflight":
		return runPreflight(ctx, args[1:], stdout, stderr)
	case "status":
		return runStatus(ctx, args[1:], stdout, stderr)
	case "run":
		return runCopyIn(ctx, args[1:], stdout, stderr)
	case "reconcile":
		return runReconcile(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		writeUsage(stdout)
		return nil
	default:
		writeUsage(stderr)
		return fmt.Errorf("%w: unknown subcommand %q", ErrUsage, args[0])
	}
}

func writeUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage: cpa-manager-plus upgrade <subcommand>")
	_, _ = fmt.Fprintln(w, "  preflight --legacy <dir> --source-version <vX.Y.Z> [--target-dir DIR] [--scratch-dir DIR] [--json]")
	_, _ = fmt.Fprintln(w, "                                     inspect a stopped 1.x source; writes nothing")
	_, _ = fmt.Fprintln(w, "                                     --target-dir is required when the Manager database path cannot be derived")
	_, _ = fmt.Fprintln(w, "  run --legacy <dir> --source-version <vX.Y.Z> [--target <dir>] [--bundle <dir>] [--scratch-dir DIR] [--json]")
	_, _ = fmt.Fprintln(w, "                                     back up and copy a stopped 1.x source into the v2 data root")
	_, _ = fmt.Fprintln(w, "  reconcile --legacy <dir> --source-version <vX.Y.Z> [--target <dir>] [--bundle <dir>] [--scratch-dir DIR] [--json]")
	_, _ = fmt.Fprintln(w, "            [--acknowledge <YYYY-MM-DD>]... [--acknowledge-parity-gaps]")
	_, _ = fmt.Fprintln(w, "                                     compare the copied store with the 1.x source and mark")
	_, _ = fmt.Fprintln(w, "                                     the days that can never be completed again")
	_, _ = fmt.Fprintln(w, "  status [--json] [--db-path PATH] [--scratch-dir DIR]")
	_, _ = fmt.Fprintln(w, "                                     read the persisted upgrade operation")
	_, _ = fmt.Fprintln(w, "  --scratch-dir must be outside the legacy source root and the target root.")
	_, _ = fmt.Fprintln(w, "                                     the default OS temporary directory is checked the same way")
	_, _ = fmt.Fprintln(w, "Exit codes: 0 ready, 1 blocked or a refusal that needs a decision (the v2 data root in use, an unacknowledged day), 2 usage, environment or retryable error.")
}

func runPreflight(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("upgrade preflight", flag.ContinueOnError)
	flags.SetOutput(stderr)
	legacy := flags.String("legacy", "", "1.x source data directory (usage.sqlite, data.key, usage-archives/)")
	sourceVersion := flags.String("source-version", "", "1.x release the source was running (required; the database does not record it)")
	targetDir := flags.String("target-dir", "", "directory on the volume that will receive the upgrade copy; defaults to the Manager data directory")
	scratchDir := flags.String("scratch-dir", "", "directory for the transient snapshot copy; defaults to the OS temporary directory")
	asJSON := flags.Bool("json", false, "print the report as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*legacy) == "" {
		return fmt.Errorf("%w: --legacy <dir> is required and no positional arguments are accepted", ErrUsage)
	}
	if strings.TrimSpace(*targetDir) == "" {
		// The disk requirement belongs to the Manager volume, not to the source.
		// If that path cannot be derived there is nothing meaningful to measure:
		// probing the working directory would report a volume the upgrade never
		// touches, so the operator has to say where the copy will land.
		managerDatabase, err := resolveManagerDatabasePath("")
		if err != nil {
			return fmt.Errorf("%w: pass --target-dir; the Manager database path could not be derived: %v", ErrUsage, err)
		}
		*targetDir = filepath.Dir(managerDatabase)
	}
	report, err := upgradeservice.Preflight(ctx, upgradeservice.Options{
		LegacyRoot:      *legacy,
		DeclaredVersion: *sourceVersion,
		TargetDir:       *targetDir,
		ScratchDir:      *scratchDir,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		if err := json.NewEncoder(stdout).Encode(report); err != nil {
			return err
		}
	} else {
		writePreflightSummary(stdout, report)
	}
	if !report.Ready {
		return ErrBlocked
	}
	return nil
}

func writePreflightSummary(w io.Writer, report upgradeservice.Report) {
	_, _ = fmt.Fprintf(w, "source kind: %s\n", report.SourceKind)
	_, _ = fmt.Fprintf(w, "source version: %s\n", report.SourceVersion)
	_, _ = fmt.Fprintf(w, "fingerprint: %s\n", report.Fingerprint)
	_, _ = fmt.Fprintln(w, "checks:")
	for _, check := range report.Checks {
		line := fmt.Sprintf("  %s %s", check.Name, check.Status)
		if check.Code != "" {
			line += " " + check.Code
		}
		if check.Detail != "" {
			line += ": " + check.Detail
		}
		_, _ = fmt.Fprintln(w, line)
	}
	if len(report.MissingTables) != 0 {
		_, _ = fmt.Fprintf(w, "missing tables: %s\n", strings.Join(report.MissingTables, ","))
	}
	if !report.ParityGaps.Applicable && report.ParityGaps.Reason != "" {
		_, _ = fmt.Fprintf(w, "parity: not applicable (%s)\n", report.ParityGaps.Reason)
	}
	if len(report.ParityGaps.AffectedDates) != 0 {
		_, _ = fmt.Fprintf(w, "parity gaps on %s\n", strings.Join(report.ParityGaps.AffectedDates, ","))
	}
	_, _ = fmt.Fprintf(w, "ready: %t\n", report.Ready)
}

type statusOutput struct {
	Recorded  bool                    `json:"recorded"`
	Operation *model.UpgradeOperation `json:"operation,omitempty"`
}

func runStatus(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("upgrade status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbPath := flags.String("db-path", "", "Manager SQLite path; defaults to the Manager configuration")
	scratchDir := flags.String("scratch-dir", "", "directory for the transient snapshot copy; defaults to the OS temporary directory")
	asJSON := flags.Bool("json", false, "print the operation as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("%w: no positional arguments are accepted", ErrUsage)
	}
	path, err := resolveManagerDatabasePath(*dbPath)
	if err != nil {
		return err
	}
	// A scratch copy under the Manager data directory would write the whole
	// database back into the volume this operation is upgrading. An unset
	// --scratch-dir means the OS temporary directory and is checked the same
	// way, so a host whose TMPDIR points into that volume is refused instead of
	// leaving a full copy behind.
	if err := upgradeservice.ValidateScratchDir(*scratchDir, "", filepath.Dir(path)); err != nil {
		return codedFailure(upgradeservice.CodeSnapshotFailed, err)
	}
	// A read-only open of a WAL database creates -shm/-wal next to it, which
	// fails on a read-only volume. The row is read from a private copy instead.
	snapshot, err := openStatusSnapshot(ctx, path, *scratchDir)
	if err != nil {
		switch {
		case errors.Is(err, upgradeservice.ErrSourceChanged):
			// The copy kept losing the race with a live Manager writer. That is
			// a retryable condition, not a usage error: nothing the operator
			// typed was wrong.
			return fmt.Errorf("%w: %s: the Manager database changed during each of %d snapshot attempts; retry when the writer is quieter: %v",
				ErrRetryable, upgradeservice.CodeSourceChanged, statusSnapshotAttempts, err)
		case errors.Is(err, upgradeservice.ErrSnapshotFailed):
			return codedFailure(upgradeservice.CodeSnapshotFailed, err)
		default:
			return err
		}
	}
	defer func() { _ = snapshot.Close() }()
	db := snapshot.DB()
	tablePresent, err := upgradeOperationTablePresent(ctx, db)
	if err != nil {
		return err
	}
	output := statusOutput{}
	if tablePresent {
		operation, found, err := upgradeoperation.New(db).Get(ctx)
		if err != nil {
			return err
		}
		if found {
			output.Recorded = true
			output.Operation = &operation
		}
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(output)
	}
	writeStatusSummary(stdout, output)
	return nil
}

func writeStatusSummary(w io.Writer, output statusOutput) {
	if !output.Recorded || output.Operation == nil {
		_, _ = fmt.Fprintln(w, "no upgrade operation recorded")
		return
	}
	operation := output.Operation
	_, _ = fmt.Fprintf(w, "stage: %s\n", operation.Stage)
	_, _ = fmt.Fprintf(w, "domains done: %s\n", strings.Join(operation.DomainsDone, ","))
	if operation.SourceKind != "" {
		_, _ = fmt.Fprintf(w, "source kind: %s\n", operation.SourceKind)
	}
	if operation.SourceVersion != "" {
		_, _ = fmt.Fprintf(w, "source version: %s\n", operation.SourceVersion)
	}
	if operation.ErrorCode != "" {
		_, _ = fmt.Fprintf(w, "error: %s\n", operation.ErrorCode)
	}
	_, _ = fmt.Fprintf(w, "updated at ms: %d\n", operation.UpdatedAtMS)
}

// runCopyIn backs up a stopped 1.x source and copies it into the v2 data root.
// The preflight gate runs first: a blocked source writes nothing at all, and the
// preflight itself reads a private snapshot of the source instead of opening it
// in place.
func runCopyIn(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("upgrade run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	legacy := flags.String("legacy", "", "1.x source data directory (usage.sqlite, data.key, usage-archives/)")
	sourceVersion := flags.String("source-version", "", "1.x release the source was running (required; the database does not record it)")
	target := flags.String("target", "", "v2 data directory; defaults to the Manager data directory when every target member lands there")
	bundle := flags.String("bundle", "", "backup destination; defaults to <target>/upgrade-backup")
	scratchDir := flags.String("scratch-dir", "", "directory for the transient preflight snapshot; defaults to the OS temporary directory")
	asJSON := flags.Bool("json", false, "print the preflight report and the copy-in result as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*legacy) == "" {
		return fmt.Errorf("%w: --legacy <dir> is required and no positional arguments are accepted", ErrUsage)
	}
	targetRoot, err := resolveTargetRoot(*target)
	if err != nil {
		return err
	}
	legacyRoot, err := filepath.Abs(strings.TrimSpace(*legacy))
	if err != nil {
		return fmt.Errorf("resolve the 1.x source root: %w", err)
	}
	bundleDir := strings.TrimSpace(*bundle)
	if bundleDir == "" {
		bundleDir = upgradeservice.DefaultBundleDirectory(targetRoot)
	}
	report, err := upgradeservice.Preflight(ctx, upgradeservice.Options{
		LegacyRoot:      legacyRoot,
		DeclaredVersion: *sourceVersion,
		TargetDir:       targetRoot,
		ScratchDir:      *scratchDir,
	})
	if err != nil {
		return err
	}
	output := runOutput{Preflight: &report}
	if !report.Ready {
		writeRunOutput(stdout, *asJSON, output)
		return ErrBlocked
	}
	// The 1.x source is documented read-only for this command, so its fence is
	// taken through a read-only descriptor: the same exclusive marker, the same
	// exclusion of cooperating writers, but the source volume is never opened
	// for writing and a stopped source preserved on a read-only mount still
	// proves exclusivity.
	lock, err := processlock.AcquireExistingReadOnly(upgradeservice.SourceDatabasePath(legacyRoot))
	if err != nil {
		return fmt.Errorf("take the 1.x ownership lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	result, err := upgradeservice.CopyIn(ctx, upgradeservice.Options{
		LegacyRoot:      legacyRoot,
		DeclaredVersion: *sourceVersion,
		TargetRoot:      targetRoot,
		BundleDir:       bundleDir,
		Lock:            lock,
	})
	if err != nil {
		return err
	}
	output.Result = &result
	writeRunOutput(stdout, *asJSON, output)
	return nil
}

type runOutput struct {
	Preflight *upgradeservice.Report `json:"preflight"`
	Result    *upgradeservice.Result `json:"result,omitempty"`
}

func writeRunOutput(w io.Writer, asJSON bool, output runOutput) {
	if asJSON {
		_ = json.NewEncoder(w).Encode(output)
		return
	}
	if output.Preflight != nil {
		writePreflightSummary(w, *output.Preflight)
	}
	if output.Result == nil {
		return
	}
	result := output.Result
	_, _ = fmt.Fprintln(w, "copy-in:")
	_, _ = fmt.Fprintf(w, "  backup: %s\n", result.BackupDirectory)
	_, _ = fmt.Fprintf(w, "  backup manifest sha256: %s\n", result.BackupManifestSHA256)
	_, _ = fmt.Fprintf(w, "  source fingerprint before: %s\n", result.SourceFingerprintBefore)
	_, _ = fmt.Fprintf(w, "  source fingerprint after: %s\n", result.SourceFingerprintAfter)
	_, _ = fmt.Fprintf(w, "  source kind: %s\n", result.SourceKind)
	_, _ = fmt.Fprintf(w, "  source version: %s\n", result.SourceVersion)
	_, _ = fmt.Fprintf(w, "  stage: %s\n", result.Stage)
	_, _ = fmt.Fprintf(w, "  domains done: %s\n", strings.Join(result.DomainsDone, ","))
	_, _ = fmt.Fprintf(w, "  restored: %t\n", result.Restored)
	_, _ = fmt.Fprintf(w, "  already complete: %t\n", result.AlreadyComplete)
	_, _ = fmt.Fprintf(w, "  database: %s\n", result.TargetDatabase)
	_, _ = fmt.Fprintf(w, "  data key: %s\n", result.TargetDataKey)
	_, _ = fmt.Fprintf(w, "  archives: %s\n", result.TargetArchives)
}

// runReconcile compares the copied-in store with the stopped 1.x source. The
// preflight gate runs first, so a blocked source is never compared. A
// disagreement writes the report and exits 1: nothing was marked, and the
// report names every day that needs an explicit acknowledgement.
func runReconcile(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("upgrade reconcile", flag.ContinueOnError)
	flags.SetOutput(stderr)
	legacy := flags.String("legacy", "", "1.x source data directory (usage.sqlite, data.key, usage-archives/)")
	sourceVersion := flags.String("source-version", "", "1.x release the source was running (required; the database does not record it)")
	target := flags.String("target", "", "v2 data directory; defaults to the Manager data directory")
	bundle := flags.String("bundle", "", "backup destination of the recorded copy-in; defaults to <target>/upgrade-backup")
	scratchDir := flags.String("scratch-dir", "", "directory for the transient preflight and reconciliation copies; defaults to the OS temporary directory")
	asJSON := flags.Bool("json", false, "print the preflight report and the reconciliation as JSON")
	acknowledgeParityGaps := flags.Bool("acknowledge-parity-gaps", false,
		"accept the days the parity check reports as lost before v1.14.6 when nothing else disagrees on them; "+
			"a day with a measured mismatch or a lost rollup still needs --acknowledge")
	acknowledged := make([]string, 0, 1)
	flags.Func("acknowledge", "accept one UTC day (YYYY-MM-DD) as permanently incomplete; repeat for each day",
		func(value string) error {
			acknowledged = append(acknowledged, value)
			return nil
		})
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*legacy) == "" {
		return fmt.Errorf("%w: --legacy <dir> is required and no positional arguments are accepted", ErrUsage)
	}
	targetRoot, err := resolveTargetRoot(*target)
	if err != nil {
		return err
	}
	legacyRoot, err := filepath.Abs(strings.TrimSpace(*legacy))
	if err != nil {
		return fmt.Errorf("resolve the 1.x source root: %w", err)
	}
	bundleDir := strings.TrimSpace(*bundle)
	if bundleDir == "" {
		bundleDir = upgradeservice.DefaultBundleDirectory(targetRoot)
	}
	report, err := upgradeservice.Preflight(ctx, upgradeservice.Options{
		LegacyRoot:      legacyRoot,
		DeclaredVersion: *sourceVersion,
		TargetDir:       targetRoot,
		ScratchDir:      *scratchDir,
	})
	if err != nil {
		return err
	}
	output := reconcileOutput{Preflight: &report}
	if !report.Ready {
		writeReconcileOutput(stdout, *asJSON, output)
		return ErrBlocked
	}
	// Every refusal below still prints the report with its typed code, so a
	// script reads the code and a person reads what to do next.
	fail := func(err error) error {
		if code, ok := upgradeservice.UpgradeErrorCodeOf(err); ok {
			output.ErrorCode = string(code)
		}
		output.Error = err.Error()
		writeReconcileOutput(stdout, *asJSON, output)
		return err
	}
	// The step reads a stopped 1.x source and writes the v2 store, so it holds
	// both fences for the whole run, exactly as the copy-in holds the source one:
	// a live Manager or another offline step owns one of them, and racing it
	// would write permanent marks under a reader's feet. The source is read-only
	// for this step, so its fence is taken through a read-only descriptor: the
	// same exclusive marker and the same excluded writers, but the source is
	// never opened for writing and a stopped source preserved on a read-only
	// mount still proves exclusivity.
	sourceLock, err := processlock.AcquireExistingReadOnly(upgradeservice.SourceDatabasePath(legacyRoot))
	if err != nil {
		return fail(upgradeservice.CodedError(upgradeservice.UpgradeErrorCode(upgradeservice.CodeSourceBusy),
			"the 1.x source is held by another process; stop Manager Server and retry", err))
	}
	defer func() { _ = sourceLock.Close() }()
	targetLock, err := processlock.Acquire(upgradeservice.TargetDatabasePath(targetRoot))
	if err != nil {
		return fail(upgradeservice.CodedError(upgradeservice.UpgradeErrorTargetLocked,
			"the v2 data root is held by another process; stop Manager Server and retry", err))
	}
	defer func() { _ = targetLock.Close() }()
	reconcileReport, err := upgradeservice.Reconcile(ctx, upgradeservice.Options{
		LegacyRoot:            legacyRoot,
		TargetRoot:            targetRoot,
		BundleDir:             bundleDir,
		ScratchDir:            *scratchDir,
		Lock:                  sourceLock,
		TargetLock:            targetLock,
		Acknowledged:          acknowledged,
		AcknowledgeParityGaps: *acknowledgeParityGaps,
	})
	output.Report = &reconcileReport
	if err != nil {
		return fail(err)
	}
	writeReconcileOutput(stdout, *asJSON, output)
	return nil
}

type reconcileOutput struct {
	Preflight *upgradeservice.Report          `json:"preflight"`
	Report    *upgradeservice.ReconcileReport `json:"report,omitempty"`
	Error     string                          `json:"error,omitempty"`
	ErrorCode string                          `json:"errorCode,omitempty"`
}

func writeReconcileOutput(w io.Writer, asJSON bool, output reconcileOutput) {
	if asJSON {
		_ = json.NewEncoder(w).Encode(output)
		return
	}
	if output.Preflight != nil {
		writePreflightSummary(w, *output.Preflight)
	}
	if output.Report != nil {
		writeReconcileSummary(w, *output.Report)
	}
	if output.Error != "" {
		_, _ = fmt.Fprintf(w, "reconcile error: %s\n", output.Error)
	}
}

func writeReconcileSummary(w io.Writer, report upgradeservice.ReconcileReport) {
	_, _ = fmt.Fprintln(w, "reconcile:")
	_, _ = fmt.Fprintf(w, "  source fingerprint: %s\n", report.SourceFingerprint)
	for _, domain := range report.Domains {
		line := fmt.Sprintf("  %s: ", domain.Domain)
		if !domain.Applicable {
			line += "not applicable (" + domain.Reason + ")"
			_, _ = fmt.Fprintln(w, line)
			continue
		}
		line += fmt.Sprintf("%d day(s) compared of %d source day(s), %d mismatch(es)",
			domain.ComparedDays, domain.SourceDays, len(domain.Mismatches))
		_, _ = fmt.Fprintln(w, line)
		for _, mismatch := range domain.Mismatches {
			detail := fmt.Sprintf("    %s %s", mismatch.Day, mismatch.Field)
			if mismatch.Dimension != "" {
				detail += "[" + mismatch.Dimension + "]"
			}
			detail += fmt.Sprintf(": source %s, store %s", mismatch.Source, mismatch.Target)
			_, _ = fmt.Fprintln(w, detail)
		}
	}
	if len(report.ParityGapDays) > 0 {
		_, _ = fmt.Fprintf(w, "  parity gaps: %s\n", strings.Join(report.ParityGapDays, ","))
	}
	if len(report.AcknowledgedDays) > 0 {
		_, _ = fmt.Fprintf(w, "  acknowledged: %s\n", strings.Join(report.AcknowledgedDays, ","))
	}
	for _, day := range report.IncompleteDays {
		_, _ = fmt.Fprintf(w, "  incomplete: %s %s\n", day.Day, day.Reason)
	}
	if len(report.UnacknowledgedDays) > 0 {
		_, _ = fmt.Fprintf(w, "  needs acknowledgement: %s\n", strings.Join(report.UnacknowledgedDays, ","))
	}
	_, _ = fmt.Fprintf(w, "  newly marked: %d\n", report.NewlyMarkedDays)
	_, _ = fmt.Fprintf(w, "  recorded: %t\n", report.Recorded)
	if report.Stage != "" {
		_, _ = fmt.Fprintf(w, "  stage: %s\n", report.Stage)
		_, _ = fmt.Fprintf(w, "  domains done: %s\n", strings.Join(report.DomainsDone, ","))
	}
}

// resolveTargetRoot resolves the v2 data root of `upgrade run`.
//
// An explicit --target states the layout, so it is used as given. The Manager
// configuration is a default only when every member the copy-in writes is also
// the member the service will read: the database must be the canonical
// usage.sqlite, the data key and the archive tree must resolve inside that same
// directory, and no environment key may override the copied key. Otherwise the
// copy lands where nothing reads it — the copied data key would never decrypt
// the 1.x fields — so the run refuses and asks for --target instead of writing
// files the service ignores.
func resolveTargetRoot(override string) (string, error) {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		return trimmed, nil
	}
	cfg, err := config.LoadWithoutCreatingDefault()
	if err != nil {
		return "", fmt.Errorf("load Manager configuration: %w", err)
	}
	root := filepath.Dir(strings.TrimSpace(cfg.DBPath))
	if strings.TrimSpace(cfg.DBPath) == "" {
		return "", layoutMismatch("the Manager database path is empty")
	}
	if filepath.Base(cfg.DBPath) != upgradeservice.SourceDatabaseName {
		return "", layoutMismatch(fmt.Sprintf("the Manager database is %s, not %s",
			filepath.Base(cfg.DBPath), upgradeservice.SourceDatabaseName))
	}
	// An inline key wins over data.key at the service, so a copied data.key would
	// never be read.
	if strings.TrimSpace(cfg.DataKey) != "" {
		return "", layoutMismatch("an environment or file key overrides the copied data key")
	}
	layout := upgradeservice.LayoutForRoot(root)
	if strings.TrimSpace(cfg.DataKeyPath) == "" || !upgradeservice.SamePath(cfg.DataKeyPath, layout.DataKey) {
		return "", layoutMismatch(fmt.Sprintf("the configured data key %s is not %s",
			cfg.DataKeyPath, layout.DataKey))
	}
	if strings.TrimSpace(cfg.UsageArchiveDir) == "" || !upgradeservice.SamePath(cfg.UsageArchiveDir, layout.Archives) {
		return "", layoutMismatch(fmt.Sprintf("the configured archive directory %s is not %s",
			cfg.UsageArchiveDir, layout.Archives))
	}
	return root, nil
}

// layoutMismatch refuses a Manager layout the copy-in cannot populate. The code
// is shared with the report vocabulary so a script can tell it apart from a
// blocked source.
func layoutMismatch(detail string) error {
	return fmt.Errorf("%w: %s: %s; pass --target with the v2 data directory",
		ErrUsage, upgradeservice.CodeTargetLayoutMismatch, detail)
}

// openStatusSnapshot copies the Manager database, retrying a bounded number of
// times when the copy raced a live writer. Every other failure is returned at
// once: a missing database or an unusable scratch directory does not get better
// by waiting.
//
// This is the one place retries are allowed. Preflight stays fail-closed — a
// legacy source that is still being written must be stopped, never polled.
func openStatusSnapshot(ctx context.Context, path string, scratchDir string) (*upgradeservice.Snapshot, error) {
	delay := statusSnapshotBaseDelay
	var lastErr error
	for attempt := 1; attempt <= statusSnapshotAttempts; attempt++ {
		snapshot, err := openSnapshot(ctx, path, scratchDir)
		if err == nil {
			return snapshot, nil
		}
		if !errors.Is(err, upgradeservice.ErrSourceChanged) {
			return nil, err
		}
		lastErr = err
		if attempt == statusSnapshotAttempts {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if delay < statusSnapshotMaxDelay {
			delay *= 2
			if delay > statusSnapshotMaxDelay {
				delay = statusSnapshotMaxDelay
			}
		}
	}
	return nil, lastErr
}

// codedFailure prefixes a failure with the shared upgrade code vocabulary so the
// operator sees upgrade_snapshot_failed instead of a bare exit code.
func codedFailure(code string, cause error) error {
	return fmt.Errorf("upgrade: %s: %v", code, cause)
}

func resolveManagerDatabasePath(override string) (string, error) {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		return trimmed, nil
	}
	cfg, err := config.LoadWithoutCreatingDefault()
	if err != nil {
		return "", fmt.Errorf("load Manager configuration: %w", err)
	}
	if strings.TrimSpace(cfg.DBPath) == "" {
		return "", fmt.Errorf("%w: Manager database path is empty; pass --db-path", ErrUsage)
	}
	return cfg.DBPath, nil
}

func upgradeOperationTablePresent(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `select count(*) from sqlite_master
		where type = 'table' and name = 'upgrade_operations'`).Scan(&count); err != nil {
		return false, err
	}
	return count == 1, nil
}
