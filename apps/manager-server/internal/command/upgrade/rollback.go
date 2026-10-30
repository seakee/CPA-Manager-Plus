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
// operator and --verify proves the restarted 1.x stack itself before recording.
func runRollback(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("upgrade rollback", flag.ContinueOnError)
	flags.SetOutput(stderr)
	prepare := flags.Bool("prepare", false, "derive the rollback plan and write it under the v2 data root")
	verify := flags.Bool("verify", false, "prove the 1.x volume is back and record the rolled_back stage")
	legacy := flags.String("legacy", "", "1.x source data directory (usage.sqlite, data.key, usage-archives/)")
	target := flags.String("target", "", "v2 data directory; defaults to the Manager data directory")
	legacyImage := flags.String("legacy-image", "", "exact 1.x image reference to pin; defaults to v<source version>")
	project := flags.String("project", "", "1.x compose project that names the data volume; defaults to cpamp")
	backup := flags.String("backup", "", "recorded backup snapshot; defaults to the v2 backup directory")
	statusURL := flags.String("status-url", "", "1.x health endpoint probed by --verify; defaults to http://127.0.0.1:18317/health")
	allowFamily := flags.Bool("allow-image-family-mismatch", false,
		"accept a 1.x image from another version family than the recorded source")
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
	if *allowFamily && strings.TrimSpace(*legacyImage) == "" {
		return fmt.Errorf("%w: --allow-image-family-mismatch needs --legacy-image", ErrUsage)
	}
	targetRoot, err := resolveTargetRoot(*target)
	if err != nil {
		return err
	}
	if *prepare {
		return runRollbackPrepare(ctx, stdout, stderr, *asJSON, upgradeservice.RollbackOptions{
			LegacyRoot:               *legacy,
			TargetRoot:               targetRoot,
			TagOverride:              *legacyImage,
			Project:                  *project,
			AllowImageFamilyMismatch: *allowFamily,
		})
	}
	return runRollbackVerify(ctx, stdout, stderr, *asJSON, upgradeservice.RollbackObservation{
		LegacyRoot:               *legacy,
		TargetRoot:               targetRoot,
		BackupDir:                *backup,
		Image:                    *legacyImage,
		StatusURL:                *statusURL,
		ObservedAtMS:             time.Now().UnixMilli(),
		AllowImageFamilyMismatch: *allowFamily,
	})
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
	_, _ = fmt.Fprintf(w, "  volume mountpoint (v1 container): %s\n", plan.VolumeMountPoint)
	_, _ = fmt.Fprintf(w, "  source root: %s\n", plan.SourceRoot)
	_, _ = fmt.Fprintf(w, "  source fingerprint: %s\n", plan.SourceFingerprint)
	_, _ = fmt.Fprintf(w, "  backup manifest sha256: %s\n", plan.BackupManifestSHA256)
	_, _ = fmt.Fprintf(w, "  compose project: %s\n", plan.ComposeProject)
	_, _ = fmt.Fprintf(w, "  invocation: %s\n", plan.ComposeInvocation)
	_, _ = fmt.Fprintln(w, "  compose file (stdin):")
	for _, line := range strings.Split(strings.TrimRight(plan.ComposeFile, "\n"), "\n") {
		_, _ = fmt.Fprintf(w, "    %s\n", line)
	}
	_, _ = fmt.Fprintln(w, "  preconditions:")
	for _, precondition := range plan.Preconditions {
		_, _ = fmt.Fprintf(w, "    %s\n", precondition)
	}
	_, _ = fmt.Fprintln(w, "  health checks:")
	for _, check := range plan.HealthChecks {
		_, _ = fmt.Fprintf(w, "    %s\n", check)
	}
	_, _ = fmt.Fprintf(w, "  plan file: %s\n", plan.ReportPath)
	_, _ = fmt.Fprintf(w, "  plan digest: %s\n", plan.Digest)
	_, _ = fmt.Fprintln(w, "apply it, then record the verdict:")
	verifyCommand := fmt.Sprintf("  cpa-manager-plus upgrade rollback --verify --legacy %s --legacy-image %s --status-url %s",
		plan.SourceRoot, plan.Image, upgradeservice.LegacyHealthURL())
	if plan.ImageFamilyOverride {
		verifyCommand += " --allow-image-family-mismatch"
	}
	_, _ = fmt.Fprintln(w, verifyCommand)
}

// runRollbackVerify hands the observation to the service, which probes the 1.x
// volume, the health identity and the retained rows itself. The CLI proves
// nothing on its own: an asserted verdict would be recorded unchecked.
func runRollbackVerify(ctx context.Context, stdout, stderr io.Writer, asJSON bool,
	observation upgradeservice.RollbackObservation) error {
	operation, err := upgradeservice.RecordRollback(ctx, observation)
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
	_, _ = fmt.Fprintf(stdout, "  source root: %s\n", operation.SourceRoot)
	_, _ = fmt.Fprintf(stdout, "  source fingerprint before: %s\n", operation.SourceFingerprintBefore)
	_, _ = fmt.Fprintf(stdout, "  source fingerprint after: %s\n", operation.SourceFingerprintAfter)
	_, _ = fmt.Fprintf(stdout, "  backup manifest sha256: %s\n", operation.BackupManifestSHA256)
	if evidence := operation.Rollback; evidence != nil {
		_, _ = fmt.Fprintf(stdout, "  rollback image: %s\n", evidence.Image)
		_, _ = fmt.Fprintf(stdout, "  rollback health endpoint: %s\n", evidence.StatusURL)
		_, _ = fmt.Fprintf(stdout, "  observed source fingerprint: %s\n", evidence.SourceFingerprint)
		_, _ = fmt.Fprintf(stdout, "  observed at ms: %d\n", evidence.ObservedAtMS)
	}
	_, _ = fmt.Fprintf(stdout, "  updated at ms: %d\n", operation.UpdatedAtMS)
	_, _ = fmt.Fprintln(stdout, "the console stays on the Upgrade surface until the operation is resolved")
	return nil
}
