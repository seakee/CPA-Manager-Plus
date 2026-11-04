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
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
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

// statusSnapshotAttempts is the shared snapshot retry budget. `upgrade status`
// copies a Manager database that may still be written, and the rollback proof
// copies a live 1.x database the same way; both use the policy and the budget
// the upgrade service owns. Preflight never retries: a legacy source that is
// still being written is a stop condition, not a race to win.
const (
	statusSnapshotAttempts  = upgradeservice.SnapshotAttempts
	statusSnapshotBaseDelay = upgradeservice.SnapshotBaseDelay
	statusSnapshotMaxDelay  = upgradeservice.SnapshotMaxDelay
)

// openSnapshot is a seam so a test can prove the bounded retry and its
// exhaustion without depending on a real race.
var openSnapshot = upgradeservice.OpenSnapshot

// ExitCode maps a Run result to the CLI contract: 0 ready, 1 a decision the
// operator has to look at, and 2 anything else. The decision set is the single
// table below, and every subcommand goes through it: run, status, migrate,
// rollback, reconcile and external-connect cannot disagree about the same typed
// code, because none of them chooses an exit code itself. A changed source and a
// v2 root that no longer matches its copy stay retryable at exit 2, and the
// typed code in the report or stderr says which failure it was.
//
// When a command wrote its own refusal to stderr, main.go must not log the same
// reason twice; ErrReported is the marker for that, and it never changes the
// exit code.
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

// refusalCodes is the one exit-code table for typed failures. A code listed here
// is a decision about the migration, the recorded store or the operator's CPA:
// the operator has to look at the source, the store, or the day the report
// names, so every subcommand exits 1 and the code in the report says which. A
// code absent from the table is a usage, environment or retryable failure and
// exits 2 on every subcommand. Adding a code means deciding here, once.
var refusalCodes = []upgradeservice.UpgradeErrorCode{
	// The source or the v2 data root is not this run's to read or write: a live
	// writer holds it, or the offline store already holds an operation that the
	// step may not overwrite.
	upgradeservice.UpgradeErrorCode(upgradeservice.CodeSourceBusy),
	upgradeservice.UpgradeErrorV2Running,
	upgradeservice.UpgradeErrorTargetLocked,
	// The recorded copy-in or the reconciliation is a decision about what the
	// store holds and which days it can never complete again.
	upgradeservice.UpgradeErrorReconcileMismatch,
	upgradeservice.UpgradeErrorReconcileTargetIncomplete,
	upgradeservice.UpgradeErrorReconcileNothingCompared,
	upgradeservice.UpgradeErrorReconcileNotCopiedIn,
	upgradeservice.UpgradeErrorReconcileStageClosed,
	upgradeservice.UpgradeErrorReconcileRecordIncomplete,
	// The offline store cannot be read or migrated without the operator: the
	// row predates this build, a live Manager holds the lock, or the store never
	// recorded the copy-in the migration needs.
	upgradeservice.UpgradeErrorStoreSchemaOutdated,
	upgradeservice.UpgradeErrorStoreLocked,
	upgradeservice.UpgradeErrorMigrateNotCopiedIn,
	// A rollback plan or verdict the recorded evidence does not support. These
	// codes have no other subcommand; they were exit 1 before the table existed
	// and stay there.
	upgradeservice.UpgradeErrorRollbackNotApplicable,
	upgradeservice.UpgradeErrorRollbackUnproven,
	upgradeservice.UpgradeErrorRollbackImageUnpinned,
	upgradeservice.UpgradeErrorRollbackImageFamily,
	upgradeservice.UpgradeErrorRollbackVolumeUnbound,
	upgradeservice.UpgradeErrorRollbackRootsOverlap,
	upgradeservice.UpgradeErrorRollbackEvidenceStale,
	upgradeservice.UpgradeErrorRollbackProbeEndpoint,
	upgradeservice.UpgradeErrorRollbackIdentityForeign,
	upgradeservice.UpgradeErrorRollbackProbeSelf,
	upgradeservice.UpgradeErrorRollbackPortCollision,
	upgradeservice.UpgradeErrorRollbackPortBusy,
	upgradeservice.UpgradeErrorRollbackV2Running,
	// A decision about the user's CPA: a v7 target, a rejected management key, a
	// target that moved, or a document that is not the v8 contract. The owner
	// has to act, so it is never read as a transient error. An unreachable CPA
	// and a store the step cannot run from are environment conditions and stay
	// out of the table.
	upgradeservice.UpgradeErrorExternalVersionUnsupported,
	upgradeservice.UpgradeErrorExternalConfigDocumentInvalid,
	upgradeservice.UpgradeErrorExternalManagementRejected,
	upgradeservice.UpgradeErrorExternalTargetMoved,
	upgradeservice.UpgradeErrorExternalV2Running,
	// A decision about the stopped 1.x stack's managed CPA state or about the
	// destination it imports into: a held source marker, missing or unreadable
	// managed state, a v7 source without a named release, absent client keys, no
	// plugin decision, a stage the import may not be entered from, a source root
	// that is not the one the copy-in read, a destination request that changed,
	// a destination that answers as another product, or an inventory the run could
	// not prove it copied faithfully. Each one needs the operator to act, so none
	// is read as a transient error. A destination that is not ready or cannot be
	// reached, and an import the destination simply has not been deployed for, are
	// environment conditions and stay out of the table.
	upgradeservice.UpgradeErrorCPASourceLocked,
	upgradeservice.UpgradeErrorCPASourceMissing,
	upgradeservice.UpgradeErrorCPAConfigInvalid,
	upgradeservice.UpgradeErrorCPAVersionRequired,
	upgradeservice.UpgradeErrorCPAVersionUnsupported,
	upgradeservice.UpgradeErrorCPAClientKeysMissing,
	upgradeservice.UpgradeErrorCPAPluginChoiceRequired,
	upgradeservice.UpgradeErrorCPAStageConflict,
	upgradeservice.UpgradeErrorCPASourceRootMismatch,
	upgradeservice.UpgradeErrorCPADestinationKeyRequired,
	upgradeservice.UpgradeErrorCPADestinationRequestChanged,
	upgradeservice.UpgradeErrorCPAConnectionConflict,
	upgradeservice.UpgradeErrorCPAImportTargetForeign,
	upgradeservice.UpgradeErrorCPAImportUnproven,
	// A decision about the recorded operation the completion gate proves: a
	// position it may not run from, a held fence, a source that is not mounted or
	// not the volume the copy-in read, a reconciliation it cannot accept, a
	// domain, binding or connection the operation does not hold. Each one needs
	// the operator to act, so none is read as a transient error. A revision
	// conflict — upgrade_verify_conflict — is the one retryable verdict and stays
	// out of the table: it says another writer changed the row, so running the
	// verification again is the whole repair.
	upgradeservice.UpgradeErrorVerifyNotApplicable,
	upgradeservice.UpgradeErrorVerifyV2Running,
	upgradeservice.UpgradeErrorVerifySourceUnavailable,
	upgradeservice.UpgradeErrorVerifySourceMismatch,
	upgradeservice.UpgradeErrorVerifyReconciliationInvalid,
	upgradeservice.UpgradeErrorVerifyReconciliationUnacknowledged,
	upgradeservice.UpgradeErrorVerifyDomainsIncomplete,
	upgradeservice.UpgradeErrorVerifyBindingMissing,
	upgradeservice.UpgradeErrorVerifyBindingDrifted,
	upgradeservice.UpgradeErrorVerifyConnectionUnproven,
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
	case "migrate":
		return runMigrate(ctx, args[1:], stdout, stderr)
	case "run":
		return runCopyIn(ctx, args[1:], stdout, stderr)
	case "reconcile":
		return runReconcile(ctx, args[1:], stdout, stderr)
	case "rollback":
		return runRollback(ctx, args[1:], stdout, stderr)
	case "external-connect":
		return runExternalConnect(ctx, args[1:], stdout, stderr)
	case "cpa-import":
		return runCPAImport(ctx, args[1:], stdout, stderr)
	case "verify":
		return runVerify(ctx, args[1:], stdout, stderr)
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
	_, _ = fmt.Fprintln(w, "                                      inspect a stopped 1.x source; writes nothing")
	_, _ = fmt.Fprintln(w, "                                      --target-dir is required when the Manager database path cannot be derived")
	_, _ = fmt.Fprintln(w, "  run --legacy <dir> --source-version <vX.Y.Z> [--target <dir>] [--bundle <dir>] [--scratch-dir DIR] [--json]")
	_, _ = fmt.Fprintln(w, "                                      back up and copy a stopped 1.x source into the v2 data root")
	_, _ = fmt.Fprintln(w, "  reconcile --legacy <dir> --source-version <vX.Y.Z> [--target <dir>] [--bundle <dir>] [--scratch-dir DIR] [--json]")
	_, _ = fmt.Fprintln(w, "            [--acknowledge <YYYY-MM-DD>]... [--acknowledge-parity-gaps]")
	_, _ = fmt.Fprintln(w, "                                      compare the copied store with the 1.x source and mark")
	_, _ = fmt.Fprintln(w, "                                      the days that can never be completed again")
	_, _ = fmt.Fprintln(w, "  status [--json] [--db-path PATH] [--scratch-dir DIR]")
	_, _ = fmt.Fprintln(w, "                                      read the persisted upgrade operation")
	_, _ = fmt.Fprintln(w, "  migrate [--json] [--db-path PATH]  add the current upgrade columns to an older store")
	_, _ = fmt.Fprintln(w, "                                      (the v2 Manager must be stopped; it holds the store lock)")
	_, _ = fmt.Fprintln(w, "  rollback --prepare --legacy <dir> [--legacy-image <ref>] [--project <name>] [--port N] [--manager-addr HOST:PORT] [--json]")
	_, _ = fmt.Fprintln(w, "  rollback --verify --legacy <dir> [--backup <dir>] [--status-url <url>] [--legacy-image <ref>] [--json]")
	_, _ = fmt.Fprintln(w, "                                      plan a 1.x restart, or record a proven one")
	_, _ = fmt.Fprintln(w, "  external-connect --cpa-base-url <url> --management-key-file <path> [--target <dir>] [--cpa-version <v>] [--reprove] [--json]")
	_, _ = fmt.Fprintln(w, "                                      connect the External CPA and record its identity; a v7 CPA stops,")
	_, _ = fmt.Fprintln(w, "                                      and --reprove re-proves the recorded URL and build version to seal a rotated key")
	_, _ = fmt.Fprintln(w, "  cpa-import --legacy <dir> --plugins <true|false> [--cpa-dir <dir>] [--cpa-version <release>]")
	_, _ = fmt.Fprintln(w, "             [--cpa-auth-dir <dir>] [--target <dir>] [--destination-url <url>]")
	_, _ = fmt.Fprintln(w, "             [--destination-management-key-file <file>] [--json]")
	_, _ = fmt.Fprintln(w, "                                      import a stopped 1.x stack's managed CPA state into the")
	_, _ = fmt.Fprintln(w, "                                      v2 destination and prove it")
	_, _ = fmt.Fprintln(w, "  verify --legacy <dir> [--target <dir>] [--json]")
	_, _ = fmt.Fprintln(w, "                                      verify the recorded operation and record the one verdict the")
	_, _ = fmt.Fprintln(w, "                                      target supports: complete for an External CPA, or the position")
	_, _ = fmt.Fprintln(w, "                                      that waits for the trusted re-pin for a Managed one")
	_, _ = fmt.Fprintln(w, "The scratch copy is never the source or the target volume: --scratch-dir,")
	_, _ = fmt.Fprintln(w, "and the default OS temporary directory, are checked the same way.")
	_, _ = fmt.Fprintln(w, "Exit codes: 0 ready, 1 blocked, refused, an unacknowledged day, a held fence (1.x source or v2 data root), a rollback decision, a decision about the External CPA or the CPA import, or a verification the recorded operation does not support; 2 usage, environment or retryable error.")
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
	// Reason is the typed explanation an admission attaches to a stage that
	// restricts the surface, so the console can show why the normal surfaces are
	// closed instead of reading a generic stage.
	Reason string `json:"reason,omitempty"`
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
	// A read-only reader cannot migrate or repair the row, so the two shapes it
	// cannot read are reported through the shared store-schema mapping rather
	// than as raw SQL or a malformed row.
	operation, found, err := upgradeservice.ReadRecordFrom(ctx, db)
	if err != nil {
		return refuseTyped(stderr, err)
	}
	output := statusOutput{}
	if found {
		output.Recorded = true
		output.Operation = &operation
		// The same derivation the startup admission uses, so the console reason
		// and the log reason can never disagree about one row.
		output.Reason = string(legacypreflight.ReasonForStage(operation.Stage))
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(output)
	}
	writeStatusSummary(stdout, output)
	return nil
}

// runMigrate is the one command that adds the upgrade columns an older store
// lacks. The read-only commands name it because neither a read-only open nor
// the Upgrade-only admission can migrate the store.
func runMigrate(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("upgrade migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbPath := flags.String("db-path", "", "Manager SQLite path; defaults to the Manager configuration")
	asJSON := flags.Bool("json", false, "print the migration result as JSON")
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
	if err := upgradeservice.MigrateOperationStore(ctx, path); err != nil {
		return refuseTyped(stderr, err)
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(map[string]string{"database": path, "schema": "current"})
	}
	_, _ = fmt.Fprintf(stdout, "Manager store schema is current: %s\n", path)
	return nil
}

func writeStatusSummary(w io.Writer, output statusOutput) {
	if !output.Recorded || output.Operation == nil {
		_, _ = fmt.Fprintln(w, "no upgrade operation recorded")
		return
	}
	operation := output.Operation
	_, _ = fmt.Fprintf(w, "stage: %s\n", operation.Stage)
	if output.Reason != "" {
		_, _ = fmt.Fprintf(w, "reason: %s\n", output.Reason)
	}
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
	// A copy-in that already has a successor is not redone. The run only ever
	// owns preflight and the copy-in, so a later recorded stage is reported and
	// left alone: re-running the copy would rewrite a store a later step already
	// advanced.
	// A materializing marker is the one state that must not read the row first:
	// CopyIn resumes it from the marker and the members beside it by file
	// digest, and an in-place open would create or rewrite the WAL index the
	// resume compares. Every other state (no marker, a migrating marker, or a
	// marker that outlived its committed row) is read through a private
	// snapshot: a terminal row is reported without any write, and a copy_in row
	// reaches CopyIn, which owns the resume.
	marker, markerPresent, markerErr := copyinmarker.Read(targetRoot)
	materializing := markerErr == nil && markerPresent && marker.Phase == copyinmarker.PhaseMaterializing
	if !materializing && upgradeservice.ValidateScratchDir(*scratchDir, legacyRoot, targetRoot) == nil {
		if operation, found, err := recordedOperationAt(ctx, targetRoot, *scratchDir); err != nil {
			return refuseTyped(stderr, err)
		} else if found && stagePastCopyIn(operation.Stage) {
			output := runOutput{Operation: &operation}
			writeRunOutput(stdout, *asJSON, output)
			return nil
		}
	}
	report, err := upgradeservice.Preflight(ctx, upgradeservice.Options{
		LegacyRoot:      legacyRoot,
		DeclaredVersion: *sourceVersion,
		TargetDir:       targetRoot,
		ScratchDir:      *scratchDir,
	})
	if err != nil {
		return refuseTyped(stderr, err)
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
		return refuseTyped(stderr, err)
	}
	output.Result = &result
	// The run stops at the copy-in by contract: reconciliation is a separate,
	// authorized step, so the exact command to continue is printed rather than
	// executed.
	output.Next = nextReconcileCommand(legacyRoot, targetRoot, bundleDir, strings.TrimSpace(*sourceVersion), strings.TrimSpace(*bundle) != "")
	writeRunOutput(stdout, *asJSON, output)
	return nil
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
	// The 1.x source is a read-only input, so a target or backup destination
	// that contains it, or sits inside it, is refused here: before the preflight
	// probe, before either fence, and before anything can create a directory or
	// a lock marker below the source. A fence takes a target that does not exist
	// by creating it, which would write into the tree this step promises not to
	// change and would make the next run refuse with upgrade_source_changed.
	if err := upgradeservice.RefuseOverlappingRoots(legacyRoot, targetRoot, bundleDir); err != nil {
		return fmt.Errorf("%w: %v", ErrUsage, err)
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

type runOutput struct {
	Preflight *upgradeservice.Report  `json:"preflight,omitempty"`
	Result    *upgradeservice.Result  `json:"result,omitempty"`
	Operation *model.UpgradeOperation `json:"operation,omitempty"`
	Next      string                  `json:"next,omitempty"`
	// NextNote says what remains after Next when the printed step only reports
	// the recorded state.
	NextNote string `json:"nextNote,omitempty"`
}

// recordedOperationAt reads the operation row from a v2 data root that may not
// hold a database yet. It reads a private snapshot, never the store in place: a
// target a copy-in has not finished must keep the WAL index and members its
// resume compares. A missing store is not an error: it simply has no recorded
// operation.
func recordedOperationAt(ctx context.Context, targetRoot string, scratchDir string) (model.UpgradeOperation, bool, error) {
	return upgradeservice.ReadRecordFromSnapshot(ctx, filepath.Join(targetRoot, upgradeservice.SourceDatabaseName), scratchDir)
}

// stagePastCopyIn reports whether the recorded operation already moved beyond
// the copy-in. `upgrade run` owns preflight and the copy-in only.
func stagePastCopyIn(stage model.UpgradeStage) bool {
	switch stage {
	case model.UpgradeStageReconcile, model.UpgradeStageCPAImport,
		model.UpgradeStageExternalConnect, model.UpgradeStageVerify, model.UpgradeStageComplete:
		return true
	// A rolled-back operation already copied in and then abandoned the v2
	// root. It is reported like any later stage: the copy-in is never redone,
	// and nothing may suggest that reconciliation is still the next step.
	case model.UpgradeStageRolledBack:
		return true
	default:
		return false
	}
}

// nextReconcileCommand is the command the operator runs to continue past the
// copy-in. It is printed, never executed: reconciliation is its own step. Every
// argument is quoted for the shell, not for Go: the operator pastes this line,
// so a path containing `$` or backticks must reach the command unchanged.
func nextReconcileCommand(legacyRoot, targetRoot, bundleDir string, declaredVersion string, explicitBundle bool) string {
	command := fmt.Sprintf("cpa-manager-plus upgrade reconcile --legacy %s --target %s --source-version %s",
		upgradeservice.ShellQuote(legacyRoot), upgradeservice.ShellQuote(targetRoot), upgradeservice.ShellQuote(declaredVersion))
	if explicitBundle {
		command += fmt.Sprintf(" --bundle %s", upgradeservice.ShellQuote(bundleDir))
	}
	return command
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

func writeRunOutput(w io.Writer, asJSON bool, output runOutput) {
	if asJSON {
		_ = json.NewEncoder(w).Encode(output)
		return
	}
	if output.Preflight != nil {
		writePreflightSummary(w, *output.Preflight)
	}
	if output.Operation != nil {
		_, _ = fmt.Fprintln(w, "operation already past the copy-in; nothing was changed:")
		_, _ = fmt.Fprintf(w, "  stage: %s\n", output.Operation.Stage)
		_, _ = fmt.Fprintf(w, "  domains done: %s\n", strings.Join(output.Operation.DomainsDone, ","))
		return
	}
	if output.Result == nil {
		if output.Next != "" {
			_, _ = fmt.Fprintln(w, "next:")
			_, _ = fmt.Fprintf(w, "  %s\n", output.Next)
			if output.NextNote != "" {
				_, _ = fmt.Fprintf(w, "  %s\n", output.NextNote)
			}
		}
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
	if output.Next != "" {
		_, _ = fmt.Fprintln(w, "next:")
		_, _ = fmt.Fprintf(w, "  %s\n", output.Next)
		if output.NextNote != "" {
			_, _ = fmt.Fprintf(w, "  %s\n", output.NextNote)
		}
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
	return upgradeservice.OpenSnapshotWithRetry(ctx, openSnapshot, path, scratchDir)
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
