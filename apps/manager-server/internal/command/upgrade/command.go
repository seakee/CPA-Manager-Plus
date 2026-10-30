// Package upgrade is the operator entry point of the v2 Upgrade operation.
//
// "upgrade preflight" inspects a stopped 1.x source and writes nothing, not
// even inside the source root. "upgrade status" reads the single
// upgrade_operations row from the Manager database in read-only mode, so both
// subcommands work when the volumes are not writable. "upgrade run" performs
// the backup and copy-in while the 1.x server is stopped: it refuses a blocked
// preflight, writes only inside the v2 data root and records the outcome.
// "upgrade reconcile" compares the copied store against the stopped source and
// marks the days that can never be completed again; it refuses to mark anything
// the operator has not acknowledged.
package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

var (
	// ErrUsage marks a caller mistake: bad flags, missing arguments.
	ErrUsage = errors.New("upgrade: usage")
	// ErrBlocked marks a complete but blocked preflight report. The report was
	// already written; only the exit code says the source is not ready.
	ErrBlocked = errors.New("upgrade preflight is blocked")
)

// ExitCode maps a Run result to the CLI contract: 0 ready, 1 blocked or
// unacknowledged mismatch, 2 anything else.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrBlocked), errors.Is(err, upgradeservice.ErrReconcileMismatch):
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
	_, _ = fmt.Fprintln(w, "  preflight --legacy <dir> [--json]   inspect a stopped 1.x source; writes nothing")
	_, _ = fmt.Fprintln(w, "  run --legacy <dir> [--target <dir>] [--bundle <dir>] [--json]")
	_, _ = fmt.Fprintln(w, "                                      back up and copy a stopped 1.x source into the v2 data root")
	_, _ = fmt.Fprintln(w, "  reconcile --legacy <dir> [--target <dir>] [--bundle <dir>] [--json]")
	_, _ = fmt.Fprintln(w, "            [--acknowledge <YYYY-MM-DD>]... [--acknowledge-parity-gaps]")
	_, _ = fmt.Fprintln(w, "                                      compare the copied store with the 1.x source and mark")
	_, _ = fmt.Fprintln(w, "                                      the days that can never be completed again")
	_, _ = fmt.Fprintln(w, "  status [--json] [--db-path PATH]    read the persisted upgrade operation")
	_, _ = fmt.Fprintln(w, "Exit codes: 0 ready, 1 blocked or unacknowledged mismatch, 2 usage or environment error.")
}

func runPreflight(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("upgrade preflight", flag.ContinueOnError)
	flags.SetOutput(stderr)
	legacy := flags.String("legacy", "", "1.x source data directory (usage.sqlite, data.key, usage-archives/)")
	asJSON := flags.Bool("json", false, "print the report as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*legacy) == "" {
		return fmt.Errorf("%w: --legacy <dir> is required and no positional arguments are accepted", ErrUsage)
	}
	report, err := upgradeservice.Preflight(ctx, upgradeservice.Options{LegacyRoot: *legacy})
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
	operation, found, err := upgradeservice.ReadRecord(ctx, path)
	if err != nil {
		return err
	}
	output := statusOutput{Recorded: found}
	if found {
		output.Operation = &operation
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

// runCopyIn backs up a stopped 1.x source and copies it into the v2 data root.
// The preflight gate runs first: a blocked source writes nothing at all.
func runCopyIn(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("upgrade run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	legacy := flags.String("legacy", "", "1.x source data directory (usage.sqlite, data.key, usage-archives/)")
	target := flags.String("target", "", "v2 data directory; defaults to the Manager data directory")
	bundle := flags.String("bundle", "", "backup destination; defaults to <target>/upgrade-backup")
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
	report, err := upgradeservice.Preflight(ctx, upgradeservice.Options{LegacyRoot: legacyRoot})
	if err != nil {
		return err
	}
	output := runOutput{Preflight: &report}
	if !report.Ready {
		writeRunOutput(stdout, *asJSON, output)
		return ErrBlocked
	}
	lock, err := processlock.AcquireExisting(upgradeservice.SourceDatabasePath(legacyRoot))
	if err != nil {
		return fmt.Errorf("take the 1.x ownership lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	result, err := upgradeservice.CopyIn(ctx, upgradeservice.Options{
		LegacyRoot: legacyRoot,
		TargetRoot: targetRoot,
		BundleDir:  bundleDir,
		Lock:       lock,
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
	target := flags.String("target", "", "v2 data directory; defaults to the Manager data directory")
	bundle := flags.String("bundle", "", "backup destination of the recorded copy-in; defaults to <target>/upgrade-backup")
	asJSON := flags.Bool("json", false, "print the preflight report and the reconciliation as JSON")
	acknowledgeParityGaps := flags.Bool("acknowledge-parity-gaps", false,
		"accept every day the parity check reports as lost before v1.14.6")
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
	report, err := upgradeservice.Preflight(ctx, upgradeservice.Options{LegacyRoot: legacyRoot})
	if err != nil {
		return err
	}
	output := reconcileOutput{Preflight: &report}
	if !report.Ready {
		writeReconcileOutput(stdout, *asJSON, output)
		return ErrBlocked
	}
	reconcileReport, err := upgradeservice.Reconcile(ctx, upgradeservice.Options{
		LegacyRoot:            legacyRoot,
		TargetRoot:            targetRoot,
		BundleDir:             bundleDir,
		Acknowledged:          acknowledged,
		AcknowledgeParityGaps: *acknowledgeParityGaps,
	})
	output.Report = &reconcileReport
	if err != nil {
		if code, ok := upgradeservice.UpgradeErrorCodeOf(err); ok {
			output.ErrorCode = string(code)
		}
		output.Error = err.Error()
		writeReconcileOutput(stdout, *asJSON, output)
		return err
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
		line += fmt.Sprintf("%d day(s), %d mismatch(es)", domain.ComparedDays, len(domain.Mismatches))
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

// resolveTargetRoot resolves the v2 data root. The Manager configuration is
// the default only when its database is the canonical usage.sqlite; any other
// database name would be copied to the wrong file, so it must be explicit.
func resolveTargetRoot(override string) (string, error) {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		return trimmed, nil
	}
	cfg, err := config.LoadWithoutCreatingDefault()
	if err != nil {
		return "", fmt.Errorf("load Manager configuration: %w", err)
	}
	if strings.TrimSpace(cfg.DBPath) == "" {
		return "", fmt.Errorf("%w: Manager database path is empty; pass --target", ErrUsage)
	}
	if filepath.Base(cfg.DBPath) != upgradeservice.SourceDatabaseName {
		return "", fmt.Errorf("%w: the Manager database is not %s; pass --target", ErrUsage, upgradeservice.SourceDatabaseName)
	}
	return filepath.Dir(cfg.DBPath), nil
}
