// Package upgrade is the operator entry point of the v2 Upgrade operation.
//
// "upgrade preflight" inspects a stopped 1.x source and writes nothing, not
// even inside the source root. "upgrade status" reads the single
// upgrade_operations row from the Manager database in read-only mode, so both
// subcommands work when the volumes are not writable. "upgrade run" performs
// the backup and copy-in while the 1.x server is stopped: it refuses a blocked
// preflight, writes only inside the v2 data root and records the outcome.
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

// ExitCode maps a Run result to the CLI contract: 0 ready, 1 blocked,
// 2 anything else.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrBlocked), errors.Is(err, ErrRefused):
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
	_, _ = fmt.Fprintln(w, "  preflight --legacy <dir> [--json]   inspect a stopped 1.x source; writes nothing")
	_, _ = fmt.Fprintln(w, "  run --legacy <dir> [--target <dir>] [--bundle <dir>] [--json]")
	_, _ = fmt.Fprintln(w, "                                      back up and copy a stopped 1.x source into the v2 data root")
	_, _ = fmt.Fprintln(w, "  status [--json] [--db-path PATH]    read the persisted upgrade operation")
	_, _ = fmt.Fprintln(w, "  rollback --prepare --legacy <dir> [--legacy-image <ref>] [--project <name>] [--port N] [--manager-addr HOST:PORT] [--json]")
	_, _ = fmt.Fprintln(w, "  rollback --verify --legacy <dir> [--backup <dir>] [--status-url <url>] [--legacy-image <ref>] [--json]")
	_, _ = fmt.Fprintln(w, "                                      plan a 1.x restart, or record a proven one")
	_, _ = fmt.Fprintln(w, "Exit codes: 0 ready, 1 blocked or refused, 2 usage or environment error.")
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
		return refuseTyped(stderr, err)
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
	// A copy-in that already has a successor is not redone. The run only ever
	// owns preflight and the copy-in, so a later recorded stage is reported and
	// left alone: re-running the copy would rewrite a store a later step already
	// advanced.
	if operation, found, err := recordedOperationAt(ctx, targetRoot); err != nil {
		return err
	} else if found && stagePastCopyIn(operation.Stage) {
		output := runOutput{Operation: &operation}
		writeRunOutput(stdout, *asJSON, output)
		return nil
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
