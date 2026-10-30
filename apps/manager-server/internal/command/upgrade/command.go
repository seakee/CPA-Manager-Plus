// Package upgrade is the operator entry point of the v2 Upgrade operation.
//
// "upgrade preflight" inspects a stopped 1.x source and writes nothing, not
// even inside the source root. "upgrade status" reads the single
// upgrade_operations row from a private snapshot of the Manager database.
// Neither subcommand opens a database in place, so both work when the volumes
// are not writable.
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
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
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

// ExitCode maps a Run result to the CLI contract: 0 ready, 1 blocked,
// 2 anything else.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrBlocked):
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
	_, _ = fmt.Fprintln(w, "  status [--json] [--db-path PATH] [--scratch-dir DIR]")
	_, _ = fmt.Fprintln(w, "                                     read the persisted upgrade operation")
	_, _ = fmt.Fprintln(w, "  --scratch-dir must be outside the legacy source root and the target root.")
	_, _ = fmt.Fprintln(w, "Exit codes: 0 ready, 1 blocked, 2 usage, environment or retryable error.")
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
	// database back into the volume this operation is upgrading.
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
