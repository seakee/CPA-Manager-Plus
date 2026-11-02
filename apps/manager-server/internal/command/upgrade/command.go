// Package upgrade is the operator entry point of the v2 Upgrade operation.
//
// "upgrade preflight" inspects a stopped 1.x source and writes nothing, not
// even inside the source root. "upgrade status" reads the single
// upgrade_operations row from a private snapshot of the Manager database.
// Neither subcommand opens a database in place, so both work when the volumes
// are not writable. "upgrade run" performs the backup and copy-in while the 1.x
// server is stopped: it refuses a blocked preflight, writes only inside the v2
// data root and records the outcome.
package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
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

// ExitCode maps a Run result to the CLI contract: 0 ready, 1 blocked by a
// precondition outside this run's control, 2 anything else.
//
// A held v2 data root lock reports 1, like a blocked preflight: nothing the
// operator typed was wrong and nothing was written, but the run cannot proceed
// until the other Manager process is stopped. Usage and environment failures
// stay 2.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrBlocked), errors.Is(err, ErrRefused), errors.Is(err, upgradeservice.ErrV2Running):
		return 1
	default:
		return 2
	}
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
	case "rollback":
		return runRollback(ctx, args[1:], stdout, stderr)
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
	_, _ = fmt.Fprintln(w, "  status [--json] [--db-path PATH] [--scratch-dir DIR]")
	_, _ = fmt.Fprintln(w, "                                     read the persisted upgrade operation")
	_, _ = fmt.Fprintln(w, "  migrate [--json] [--db-path PATH]  add the current upgrade columns to an older store")
	_, _ = fmt.Fprintln(w, "                                     (the v2 Manager must be stopped; it holds the store lock)")
	_, _ = fmt.Fprintln(w, "  rollback --prepare --legacy <dir> [--legacy-image <ref>] [--project <name>] [--port N] [--manager-addr HOST:PORT] [--json]")
	_, _ = fmt.Fprintln(w, "  rollback --verify --legacy <dir> [--backup <dir>] [--status-url <url>] [--legacy-image <ref>] [--json]")
	_, _ = fmt.Fprintln(w, "                                     plan a 1.x restart, or record a proven one")
	_, _ = fmt.Fprintln(w, "  --scratch-dir must be outside the legacy source root and the target root.")
	_, _ = fmt.Fprintln(w, "                                     the default OS temporary directory is checked the same way")
	_, _ = fmt.Fprintln(w, "Exit codes: 0 ready, 1 blocked, refused or the v2 data root is in use, 2 usage, environment or retryable error.")
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
	// A copy-in that is still in flight is resumed by CopyIn, which reads the
	// marker and compares the members beside it by file digest, never by opening
	// the database. Reading the operation row first would open the materializing
	// target's WAL and create its WAL index, so the resume would no longer find
	// the target the interrupted run left. The marker is therefore read first:
	// it is a bounded file, not a database open.
	if _, markerPresent, markerErr := copyinmarker.Read(targetRoot); markerErr == nil && !markerPresent {
		if operation, found, err := recordedOperationAt(ctx, targetRoot); err != nil {
			return err
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
	// The run stops at the copy-in by contract: reconciliation is a separate,
	// authorized step, so the exact command to continue is printed rather than
	// executed.
	output.Next = nextReconcileCommand(legacyRoot, targetRoot, bundleDir, strings.TrimSpace(*bundle) != "")
	writeRunOutput(stdout, *asJSON, output)
	return nil
}

type runOutput struct {
	Preflight *upgradeservice.Report  `json:"preflight,omitempty"`
	Result    *upgradeservice.Result  `json:"result,omitempty"`
	Operation *model.UpgradeOperation `json:"operation,omitempty"`
	Next      string                  `json:"next,omitempty"`
}

// recordedOperationAt reads the operation row from a v2 data root that may not
// hold a database yet. A missing store is not an error: it simply has no
// recorded operation.
func recordedOperationAt(ctx context.Context, targetRoot string) (model.UpgradeOperation, bool, error) {
	databasePath := filepath.Join(targetRoot, upgradeservice.SourceDatabaseName)
	if _, err := os.Lstat(databasePath); err != nil {
		if os.IsNotExist(err) {
			return model.UpgradeOperation{}, false, nil
		}
		return model.UpgradeOperation{}, false, err
	}
	return upgradeservice.ReadRecord(ctx, databasePath)
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
// copy-in. It is printed, never executed: reconciliation is its own step.
func nextReconcileCommand(legacyRoot, targetRoot, bundleDir string, explicitBundle bool) string {
	command := fmt.Sprintf("cpa-manager-plus upgrade reconcile --legacy %q --target %q", legacyRoot, targetRoot)
	if explicitBundle {
		command += fmt.Sprintf(" --bundle %q", bundleDir)
	}
	return command
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
		_, _ = fmt.Fprintln(w, "next (reconciliation is a separate step):")
		_, _ = fmt.Fprintf(w, "  %s\n", output.Next)
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
