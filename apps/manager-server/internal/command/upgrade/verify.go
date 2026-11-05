package upgrade

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

// runVerify runs the one completion gate of the operation.
//
// It is the operator's half of the same gate the Upgrade wizard calls: both
// reach upgradeservice.Verify, so a verification recorded from the command line
// and one recorded from the page are the same verdict over the same evidence.
// The 1.x source is required because the fingerprint proves the volume the
// copy-in read was not written; a run without it is a typed refusal, never a
// verification that skipped the observation.
//
// The command writes the one position the gate recorded and, for a Managed
// operation, the reason it may not be promoted. A stage of `verify` is a
// success of this step, not a failure: the exit code stays 0 and the printed
// instruction names the step the operator waits for.
func runVerify(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("upgrade verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	legacy := flags.String("legacy", "", "1.x source data directory the operation copied (required; the fingerprint proves it was not written)")
	target := flags.String("target", "", "v2 data directory; defaults to the Manager data directory")
	asJSON := flags.Bool("json", false, "print the result as JSON")
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
	result, err := upgradeservice.Verify(ctx, upgradeservice.VerifyOptions{
		LegacyRoot: strings.TrimSpace(*legacy),
		TargetRoot: targetRoot,
	})
	if err != nil {
		// A refusal records nothing, so it reports nothing about the operation:
		// the code and the message are the whole answer.
		output := verifyOutput{Error: err.Error()}
		if code, ok := upgradeservice.UpgradeErrorCodeOf(err); ok {
			output.ErrorCode = string(code)
		}
		writeVerifyOutput(stdout, *asJSON, output)
		return refuseTyped(stderr, err)
	}
	writeVerifyOutput(stdout, *asJSON, verifyOutput{Result: &result})
	return nil
}

type verifyOutput struct {
	Result    *upgradeservice.VerifyResult `json:"result,omitempty"`
	Error     string                       `json:"error,omitempty"`
	ErrorCode string                       `json:"errorCode,omitempty"`
}

// writeVerifyOutput prints the outcome once. The verification evidence itself is
// not printed: the recorded digest is a fingerprint of the operator's volume,
// and this line is a report, not an export of the record.
func writeVerifyOutput(w io.Writer, asJSON bool, output verifyOutput) {
	if asJSON {
		_ = json.NewEncoder(w).Encode(output)
		return
	}
	if output.Result != nil {
		result := output.Result
		_, _ = fmt.Fprintln(w, "upgrade verify:")
		if result.AlreadyVerified {
			_, _ = fmt.Fprintln(w, "  already verified; nothing was written")
		}
		_, _ = fmt.Fprintf(w, "  stage: %s\n", result.Stage)
		_, _ = fmt.Fprintf(w, "  target: %s\n", result.TargetMode)
		_, _ = fmt.Fprintf(w, "  domains done: %s\n", strings.Join(result.DomainsDone, ","))
		if result.Complete {
			_, _ = fmt.Fprintln(w, "  the operation is complete; the normal product starts on the next start")
		} else if result.Reason != "" {
			// Both target modes complete through this gate, so a row that is not
			// complete carries the refusal that stopped it, never a waiting
			// position: there is no state the CLI reports as "waiting".
			_, _ = fmt.Fprintf(w, "  %s\n", result.Reason)
		}
		if len(result.ManualItems) > 0 {
			_, _ = fmt.Fprintf(w, "  manual items: %d recorded\n", len(result.ManualItems))
			for _, item := range result.ManualItems {
				_, _ = fmt.Fprintf(w, "    %s\n", item)
			}
		}
		return
	}
	if output.ErrorCode != "" {
		_, _ = fmt.Fprintf(w, "upgrade verify refused: %s\n", output.ErrorCode)
	}
	if output.Error != "" {
		_, _ = fmt.Fprintf(w, "  %s\n", output.Error)
	}
}
