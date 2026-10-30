package upgrade

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

// runExternalConnect connects the External CPA the upgraded install points at.
//
// It proves a v8 target — the management key authenticates, the version is the
// contract this Manager writes, the model surface answers — and records the
// connection's identity and generation. A v7 target stops with the typed version
// refusal and no way past it; the exit code is the refusal contract, so a
// wrapper cannot mistake it for a transient failure.
func runExternalConnect(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("upgrade external-connect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	target := flags.String("target", "", "v2 data directory; defaults to the Manager data directory")
	baseURL := flags.String("cpa-base-url", "", "address of the CPA the upgraded install points at")
	managementKeyFile := flags.String("management-key-file", "", "file containing the External CPA management key")
	cpaVersion := flags.String("cpa-version", "", "version the CPA serves, used only when the CPA answers without one")
	asJSON := flags.Bool("json", false, "print the result as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*baseURL) == "" || strings.TrimSpace(*managementKeyFile) == "" {
		return fmt.Errorf("%w: --cpa-base-url <url> and --management-key-file <path> are required, and no positional arguments are accepted", ErrUsage)
	}
	targetRoot, err := resolveTargetRoot(*target)
	if err != nil {
		return err
	}
	managementKey, err := readManagementKeyFile(*managementKeyFile)
	if err != nil {
		return err
	}
	result, err := upgradeservice.ExternalConnect(ctx, upgradeservice.ExternalConnectOptions{
		TargetRoot:    targetRoot,
		BaseURL:       strings.TrimSpace(*baseURL),
		ManagementKey: managementKey,
		CPAVersion:    strings.TrimSpace(*cpaVersion),
	})
	output := externalConnectOutput{Result: &result}
	if err != nil {
		if code, ok := upgradeservice.UpgradeErrorCodeOf(err); ok {
			output.ErrorCode = string(code)
		}
		output.Error = err.Error()
		writeExternalConnectOutput(stdout, *asJSON, output)
		return refuseTyped(stderr, err)
	}
	writeExternalConnectOutput(stdout, *asJSON, output)
	return nil
}

type externalConnectOutput struct {
	Result    *upgradeservice.ExternalConnectResult `json:"result,omitempty"`
	Error     string                                `json:"error,omitempty"`
	ErrorCode string                                `json:"errorCode,omitempty"`
}

func writeExternalConnectOutput(w io.Writer, asJSON bool, output externalConnectOutput) {
	if asJSON {
		_ = json.NewEncoder(w).Encode(output)
		return
	}
	if output.Result != nil {
		result := output.Result
		_, _ = fmt.Fprintln(w, "external connect:")
		if result.AlreadyComplete {
			_, _ = fmt.Fprintln(w, "  already connected to this target; nothing was changed")
		}
		if result.CPAVersion != "" {
			_, _ = fmt.Fprintf(w, "  cpa version: %s (%s)\n", result.CPAVersion, result.Support)
		}
		_, _ = fmt.Fprintf(w, "  identity: %s\n", result.Identity)
		_, _ = fmt.Fprintf(w, "  generation: %s\n", result.Generation)
		_, _ = fmt.Fprintf(w, "  client keys: %d\n", result.ClientKeys)
		_, _ = fmt.Fprintf(w, "  stage: %s\n", result.Stage)
		_, _ = fmt.Fprintf(w, "  domains done: %s\n", strings.Join(result.DomainsDone, ","))
		return
	}
	if output.ErrorCode != "" {
		_, _ = fmt.Fprintf(w, "external connect refused: %s\n", output.ErrorCode)
	}
	if output.Error != "" {
		_, _ = fmt.Fprintf(w, "  %s\n", output.Error)
	}
}

// readManagementKeyFile loads the management key from a file. The key is a
// credential, so it is never a flag value: a command line is visible to every
// process on the host.
func readManagementKeyFile(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("%w: --management-key-file is required; pass the CPA management key through a file", ErrUsage)
	}
	data, err := os.ReadFile(trimmed)
	if err != nil {
		return "", fmt.Errorf("read the CPA management key file: %w", err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", fmt.Errorf("%w: the CPA management key file is empty", ErrUsage)
	}
	return key, nil
}
