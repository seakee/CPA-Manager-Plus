package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

// ErrRefused marks an Upgrade decision the operator asked for that the recorded
// state does not support: nothing was written, and the exit code is 1 rather
// than the usage error 2.
var ErrRefused = errors.New("upgrade: refused")

// refuse keeps the typed service failure in the chain so a caller can match both
// the CLI refusal and the machine code, while the exit code stays 1. The reason
// is written to stderr: an exit code alone does not tell the operator what the
// recorded state refused.
func refuse(stderr io.Writer, err error) error {
	_, _ = fmt.Fprintln(stderr, err)
	return fmt.Errorf("%w: %w", ErrRefused, err)
}

// runRollback prepares a 1.x rollback plan or records a proven one. It never
// applies a container change: the Manager runs inside a container and must not
// drive the Docker socket, so --prepare prints the exact invocation for the
// operator and --verify records what the operator observed.
func runRollback(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("upgrade rollback", flag.ContinueOnError)
	flags.SetOutput(stderr)
	prepare := flags.Bool("prepare", false, "derive the rollback plan and write it under the v2 data root")
	verify := flags.Bool("verify", false, "prove the 1.x volume is back and record the rolled_back stage")
	legacy := flags.String("legacy", "", "1.x source data directory (usage.sqlite, data.key, usage-archives/)")
	target := flags.String("target", "", "v2 data directory; defaults to the Manager data directory")
	legacyImage := flags.String("legacy-image", "", "exact 1.x image reference to pin; defaults to v<source version>")
	statusURL := flags.String("status-url", "", "1.x status endpoint probed by --verify, e.g. http://127.0.0.1:18317/status")
	asJSON := flags.Bool("json", false, "print the plan or the recorded operation as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*legacy) == "" {
		return fmt.Errorf("%w: --legacy <dir> is required and no positional arguments are accepted", ErrUsage)
	}
	if *prepare == *verify {
		return fmt.Errorf("%w: pass exactly one of --prepare or --verify", ErrUsage)
	}
	targetRoot, err := resolveTargetRoot(*target)
	if err != nil {
		return err
	}
	if *prepare {
		return runRollbackPrepare(ctx, stdout, stderr, *asJSON, upgradeservice.RollbackOptions{
			LegacyRoot:  *legacy,
			TargetRoot:  targetRoot,
			TagOverride: *legacyImage,
		})
	}
	return runRollbackVerify(ctx, stdout, stderr, *asJSON, *legacy, targetRoot, *legacyImage, *statusURL)
}

func runRollbackPrepare(ctx context.Context, stdout, stderr io.Writer, asJSON bool, options upgradeservice.RollbackOptions) error {
	plan, err := upgradeservice.PrepareRollback(ctx, options)
	if err != nil {
		return refuse(stderr, err)
	}
	if asJSON {
		return json.NewEncoder(stdout).Encode(plan)
	}
	writePlanSummary(stdout, plan)
	return nil
}

func writePlanSummary(w io.Writer, plan upgradeservice.Plan) {
	_, _ = fmt.Fprintln(w, "rollback plan:")
	_, _ = fmt.Fprintf(w, "  stage: %s\n", plan.Stage)
	_, _ = fmt.Fprintf(w, "  source version: %s\n", plan.SourceVersion)
	_, _ = fmt.Fprintf(w, "  image: %s\n", plan.Image)
	_, _ = fmt.Fprintf(w, "  volume: %s\n", plan.Volume)
	_, _ = fmt.Fprintf(w, "  source fingerprint: %s\n", plan.SourceFingerprint)
	_, _ = fmt.Fprintf(w, "  backup manifest sha256: %s\n", plan.BackupManifestSHA256)
	_, _ = fmt.Fprintf(w, "  compose project: %s\n", plan.ComposeProject)
	_, _ = fmt.Fprintf(w, "  invocation: %s\n", plan.ComposeInvocation)
	_, _ = fmt.Fprintln(w, "  compose file (stdin):")
	for _, line := range strings.Split(strings.TrimRight(plan.ComposeFile, "\n"), "\n") {
		_, _ = fmt.Fprintf(w, "    %s\n", line)
	}
	_, _ = fmt.Fprintln(w, "  health checks:")
	for _, check := range plan.HealthChecks {
		_, _ = fmt.Fprintf(w, "    %s\n", check)
	}
	_, _ = fmt.Fprintf(w, "  plan file: %s\n", plan.ReportPath)
	_, _ = fmt.Fprintf(w, "  plan digest: %s\n", plan.Digest)
	_, _ = fmt.Fprintln(w, "apply it, then record the verdict:")
	_, _ = fmt.Fprintf(w, "  cpa-manager-plus upgrade rollback --verify --legacy <dir> --status-url %s\n",
		fmt.Sprintf("http://127.0.0.1:%d/status", upgradeservice.LegacyManagerPort))
}

func runRollbackVerify(ctx context.Context, stdout, stderr io.Writer, asJSON bool,
	legacyRoot, targetRoot, legacyImage, statusURL string) error {
	// The plan's own probe is the default, so a verify that does not name a URL
	// still probes the 1.x port the plan named instead of recording on the
	// read-only open alone.
	if strings.TrimSpace(statusURL) == "" {
		statusURL = fmt.Sprintf("http://127.0.0.1:%d/status", upgradeservice.LegacyManagerPort)
	}
	// The health checks run here rather than being taken from the operator: a
	// rollback that was not observed cannot be recorded.
	readOnly, err := upgradeservice.ProbeLegacyReadOnly(ctx, legacyRoot)
	if err != nil {
		return err
	}
	if !readOnly {
		return refuse(stderr, fmt.Errorf("%w: the 1.x volume does not open read-only with a passing quick_check",
			upgradeservice.ErrRollbackUnproven))
	}
	healthy, err := upgradeservice.CheckLegacyStatus(ctx, statusURL)
	if err != nil {
		return refuse(stderr, fmt.Errorf("%w: the 1.x status probe did not answer: %v",
			upgradeservice.ErrRollbackUnproven, err))
	}
	if !healthy {
		return refuse(stderr, fmt.Errorf("%w: the 1.x status probe answered unhealthy", upgradeservice.ErrRollbackUnproven))
	}
	// The fingerprint comes from the same observation the copy-in used, so the
	// recorded volume and the observed one are compared like for like.
	report, err := upgradeservice.Preflight(ctx, upgradeservice.Options{LegacyRoot: legacyRoot})
	if err != nil {
		return err
	}
	operation, err := upgradeservice.RecordRollback(ctx, upgradeservice.RollbackEvidence{
		LegacyRoot:        legacyRoot,
		TargetRoot:        targetRoot,
		Image:             legacyImage,
		ReadOnlyOpen:      readOnly,
		StatusURL:         statusURL,
		StatusOK:          healthy,
		SourceFingerprint: report.Fingerprint,
		ObservedAtMS:      time.Now().UnixMilli(),
	})
	if err != nil {
		return refuse(stderr, err)
	}
	if asJSON {
		return json.NewEncoder(stdout).Encode(operation)
	}
	_, _ = fmt.Fprintln(stdout, "rollback recorded:")
	_, _ = fmt.Fprintf(stdout, "  stage: %s\n", operation.Stage)
	_, _ = fmt.Fprintf(stdout, "  domains done: %s\n", strings.Join(operation.DomainsDone, ","))
	_, _ = fmt.Fprintf(stdout, "  source kind: %s\n", operation.SourceKind)
	_, _ = fmt.Fprintf(stdout, "  source fingerprint before: %s\n", operation.SourceFingerprintBefore)
	_, _ = fmt.Fprintf(stdout, "  source fingerprint after: %s\n", operation.SourceFingerprintAfter)
	_, _ = fmt.Fprintf(stdout, "  backup manifest sha256: %s\n", operation.BackupManifestSHA256)
	_, _ = fmt.Fprintf(stdout, "  updated at ms: %d\n", operation.UpdatedAtMS)
	_, _ = fmt.Fprintln(stdout, "the console stays on the Upgrade surface until the operation is resolved")
	return nil
}
