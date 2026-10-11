package legacypreflight

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

var ErrBlocked = errors.New("source inspection is blocked; see JSON report")

// Run only resolves source locations and inspects them. It never dispatches the
// server, creates defaults, generates a key or opens the writable Store.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("legacy-preflight", flag.ContinueOnError)
	flags.SetOutput(stderr)
	database := flags.String("db", "", "explicit source Manager SQLite path (required)")
	configFile := flags.String("config", "", "existing source config; omitted means not-inspected")
	dataKey := flags.String("data-key", "", "source key path; defaults beside source database")
	archives := flags.String("archives", "", "source archive directory; defaults beside source database")
	stopped := flags.Bool("source-stopped", false, "assert that all source writers are stopped; never authorizes upgrade")
	timeout := flags.Duration("timeout", 10*time.Second, "inspection time budget")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*database) == "" || *timeout <= 0 {
		return errors.New("explicit --db and a positive --timeout are required; no positional arguments")
	}
	paths := legacypreflight.DefaultPaths(*database)
	paths.Config = *configFile
	if *configFile != "" {
		cfg, err := config.ResolveInspectionLocations(*configFile, *database)
		if err != nil {
			_ = json.NewEncoder(stdout).Encode(legacypreflight.Report{SourceKind: "unknown", Blocked: []string{"source-config-unreadable"}, Unknown: []string{"source-configuration"}})
			return errors.New("source config could not be inspected")
		}
		paths.DataKey, paths.Archives = cfg.DataKeyPath, cfg.UsageArchiveDir
	}
	if *dataKey != "" {
		paths.DataKey = *dataKey
	}
	if *archives != "" {
		paths.Archives = *archives
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	report, inspectionErr := legacypreflight.Inspect(ctx, paths, legacypreflight.Options{SourceStopped: *stopped})
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		return err
	}
	if inspectionErr != nil {
		return inspectionErr
	}
	if len(report.Blocked) != 0 || report.SourceKind == "unknown" || report.SourceKind == "missing-database" {
		return ErrBlocked
	}
	return nil
}

func IsHelp(err error) bool { return errors.Is(err, flag.ErrHelp) }
