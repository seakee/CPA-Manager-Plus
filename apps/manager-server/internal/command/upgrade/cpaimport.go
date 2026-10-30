package upgrade

// "upgrade cpa-import" is the Managed CPA import step at the command line.
//
// It is the one Upgrade step that writes to a CPA, so it shares the refusals
// contract of the other steps instead of inventing one: a typed refusal the
// operator has to act on exits 1, a usage or environment failure exits 2, and
// success exits 0. Nothing here reaches a CPA before the service layer's version
// gate has passed.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

// cpaImportOutput is the command's answer. It reports the run's own result and
// the recorded stage, and it never carries a management secret or a client key.
type cpaImportOutput struct {
	Result    *upgradeservice.CPAImportResult `json:"result,omitempty"`
	Error     string                          `json:"error,omitempty"`
	ErrorCode string                          `json:"errorCode,omitempty"`
	Next      string                          `json:"next,omitempty"`
}

func runCPAImport(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("upgrade cpa-import", flag.ContinueOnError)
	flags.SetOutput(stderr)
	legacy := flags.String("legacy", "", "1.x source data directory the copy-in read")
	cpaDirectory := flags.String("cpa-dir", "", "managed CPA directory; defaults to <legacy>/../legacy-cpa")
	cpaAuthDirectory := flags.String("cpa-auth-dir", "", "credential directory; defaults to the source's configured auth directory, then <cpa-dir>/auths")
	cpaVersion := flags.String("cpa-version", "", "release of the source CPA; a v7 configuration carries no version of its own")
	target := flags.String("target", "", "v2 data directory holding the recorded operation; defaults to the Manager data directory")
	destination := flags.String("destination-url", "", "destination CPA base URL; defaults to CPA_UPSTREAM_URL")
	destinationKeyFile := flags.String("destination-management-key-file", "", "file holding the management secret of an already-running destination CPA; only needed when no earlier run of this step recorded one")
	plugins := flags.String("plugins", "", "v2's plugin-runtime choice for the destination: true or false; required")
	asJSON := flags.Bool("json", false, "print the import result as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*legacy) == "" {
		return fmt.Errorf("%w: --legacy <dir> is required and no positional arguments are accepted", ErrUsage)
	}
	// The plugin choice is read as a string so "absent" and "explicitly false"
	// are different inputs: the step refuses the first and records the second.
	choice, choiceErr := parsePluginChoice(*plugins)
	if choiceErr != nil {
		return fmt.Errorf("%w: %v", ErrUsage, choiceErr)
	}
	targetRoot, err := resolveTargetRoot(*target)
	if err != nil {
		return err
	}
	destinationURL := strings.TrimSpace(*destination)
	if destinationURL == "" {
		cfg, err := config.LoadWithoutCreatingDefault()
		if err != nil {
			return fmt.Errorf("load Manager configuration: %w", err)
		}
		destinationURL = strings.TrimSpace(cfg.CPAUpstreamURL)
		if destinationURL == "" {
			return fmt.Errorf("%w: the manager configuration names no CPA upstream URL; pass --destination-url", ErrUsage)
		}
	}
	runtime, err := cpaImportRuntime()
	if err != nil {
		return err
	}
	destinationManagementKey, err := readDestinationManagementKeyFile(*destinationKeyFile)
	if err != nil {
		return err
	}
	output := cpaImportOutput{}
	result, err := upgradeservice.ImportCPA(ctx, upgradeservice.CPAImportOptions{
		LegacyRoot:               *legacy,
		CPADirectory:             strings.TrimSpace(*cpaDirectory),
		CPAAuthDirectory:         strings.TrimSpace(*cpaAuthDirectory),
		CPAVersion:               strings.TrimSpace(*cpaVersion),
		TargetRoot:               targetRoot,
		Runtime:                  runtime,
		DestinationURL:           destinationURL,
		DestinationManagementKey: destinationManagementKey,
		PluginsEnabled:           choice,
	})
	output.Result = &result
	if err != nil {
		if code, ok := upgradeservice.UpgradeErrorCodeOf(err); ok {
			output.ErrorCode = string(code)
		}
		output.Error = err.Error()
		writeCPAImportOutput(stdout, *asJSON, output)
		return refuseTyped(stderr, err)
	}
	// The import stops at cpa_import by contract. The External connection is a
	// separate, authorized step, so the exact command that continues is printed
	// rather than run.
	if string(result.Stage) == cpaImportStage() {
		output.Next = "connect the destination CPA through the Console, or run `cpa-manager-plus upgrade status` to confirm the recorded stage"
	}
	writeCPAImportOutput(stdout, *asJSON, output)
	return nil
}

// parsePluginChoice reads the required plugin flag. An absent flag is nil, which
// the service layer refuses; it is never coerced to false.
func parsePluginChoice(raw string) (*bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	value, err := strconv.ParseBool(trimmed)
	if err != nil {
		return nil, fmt.Errorf("--plugins must be true or false")
	}
	return &value, nil
}

// readDestinationManagementKeyFile reads the management secret of a destination
// this Manager did not stage. The secret comes from a file rather than a flag
// value so it never appears in the process list.
func readDestinationManagementKeyFile(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%w: read the destination management secret file: %v", ErrUsage, err)
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return "", fmt.Errorf("%w: the destination management secret file %s is empty", ErrUsage, path)
	}
	return secret, nil
}

// cpaImportRuntime builds the Runtime client the import stages the destination
// through. Only the Manager-owned Runtime can stage a destination CPA, so a
// Manager configured for an External CPA refuses before anything is read.
func cpaImportRuntime() (upgradeservice.CPAImportRuntime, error) {
	cfg, err := config.LoadWithoutCreatingDefault()
	if err != nil {
		return nil, fmt.Errorf("load Manager configuration: %w", err)
	}
	if !cfg.EmbeddedRuntimeConfigured() {
		return nil, fmt.Errorf("%w: this Manager is not configured for the Manager-owned Runtime (CPAMP_RUNTIME_URL and CPAMP_RUNTIME_TOKEN_FILE), so it cannot stage a destination CPA", ErrUsage)
	}
	return runtimeservice.NewEmbeddedClientWithTokenSource(cfg.RuntimeURL, runtimeservice.NewFileRuntimeTokenSource(cfg.RuntimeTokenFile)), nil
}

func writeCPAImportOutput(w io.Writer, asJSON bool, output cpaImportOutput) {
	if asJSON {
		_ = json.NewEncoder(w).Encode(output)
		return
	}
	if output.Result != nil {
		result := output.Result
		_, _ = fmt.Fprintln(w, "cpa import:")
		_, _ = fmt.Fprintf(w, "  source layout: %s\n", result.SourceLayout)
		_, _ = fmt.Fprintf(w, "  source version: %s\n", result.SourceVersion)
		_, _ = fmt.Fprintf(w, "  source identity: %s\n", result.SourceIdentity)
		_, _ = fmt.Fprintf(w, "  destination: %s\n", result.DestinationURL)
		_, _ = fmt.Fprintf(w, "  plugins enabled: %t\n", result.PluginsEnabled)
		_, _ = fmt.Fprintf(w, "  client keys proved: %d\n", result.ClientKeys)
		_, _ = fmt.Fprintf(w, "  copied config paths: %d\n", len(result.CopiedConfig))
		for _, path := range result.CopiedConfig {
			_, _ = fmt.Fprintf(w, "    %s\n", path)
		}
		_, _ = fmt.Fprintf(w, "  copied credentials: %d\n", len(result.CopiedCredentials))
		for _, name := range result.CopiedCredentials {
			_, _ = fmt.Fprintf(w, "    %s\n", name)
		}
		_, _ = fmt.Fprintf(w, "  manual items: %d\n", len(result.ManualItems))
		for _, item := range result.ManualItems {
			_, _ = fmt.Fprintf(w, "    %s %s\n", item.Item, item.Reason)
		}
		_, _ = fmt.Fprintf(w, "  stage: %s\n", result.Stage)
		_, _ = fmt.Fprintf(w, "  domains done: %s\n", strings.Join(result.DomainsDone, ","))
		_, _ = fmt.Fprintf(w, "  already complete: %t\n", result.AlreadyComplete)
	}
	if output.Error != "" {
		_, _ = fmt.Fprintf(w, "cpa import error: %s\n", output.Error)
	}
	if output.Next != "" {
		_, _ = fmt.Fprintln(w, "next:")
		_, _ = fmt.Fprintf(w, "  %s\n", output.Next)
	}
}

// cpaImportStage names the stage this subcommand records. It is spelled out
// rather than imported so the CLI contract reads as text an operator can grep.
func cpaImportStage() string { return "cpa_import" }
