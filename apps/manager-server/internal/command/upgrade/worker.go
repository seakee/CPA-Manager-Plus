package upgrade

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradewizard"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgradeworker"
)

const workerParentRestartDelay = 250 * time.Millisecond

func runWorker(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("upgrade worker", flag.ContinueOnError)
	flags.SetOutput(stderr)
	check := flags.Bool("check", false, "inspect the prepared target without starting a worker; print whether an incomplete upgrade exists")
	restart := flags.Bool("restart-parent", false, "allow verified activation to request controlled shutdown of this worker's launcher parent")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || (*check && *restart) {
		return fmt.Errorf("%w: worker accepts only --check or --restart-parent", ErrUsage)
	}
	target, err := resolveTargetRoot("")
	if err != nil {
		return err
	}
	if *check {
		required, err := upgradeworker.InspectRequired(ctx, target)
		if err != nil {
			return fmt.Errorf("upgrade worker check: %w", err)
		}
		return json.NewEncoder(stdout).Encode(struct {
			Required bool `json:"required"`
		}{Required: required})
	}
	cfg, err := config.LoadWithoutCreatingDefault()
	if err != nil {
		return err
	}
	socket := strings.TrimSpace(os.Getenv(upgradeworker.SocketEnvironment))
	source := strings.TrimSpace(os.Getenv(upgradeworker.SourceEnvironment))
	if socket == "" || source == "" {
		return fmt.Errorf("%w: worker socket and trusted source root are required", ErrUsage)
	}
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var (
		runtimeClient  upgradeservice.VerifyRuntime
		importRuntime  upgradeservice.CPAImportRuntime
		artifactClient upgradewizard.ArtifactRuntime
	)
	if cfg.EmbeddedRuntimeConfigured() {
		tokenSource := runtimeservice.NewFileRuntimeTokenSource(cfg.RuntimeTokenFile)
		embedded := runtimeservice.NewEmbeddedClientWithTokenSource(cfg.RuntimeURL, tokenSource)
		runtimeClient = embedded
		importRuntime = embedded
		artifactClient = runtimeservice.NewArtifactClientWithTokenSource(cfg.RuntimeURL, tokenSource)
	}

	options := upgradeworker.Options{
		Config:        cfg,
		TargetRoot:    target,
		SourceRoot:    source,
		CPARoot:       strings.TrimSpace(os.Getenv(upgradeworker.CPARootEnvironment)),
		CPAAuthRoot:   strings.TrimSpace(os.Getenv(upgradeworker.CPAAuthEnvironment)),
		CPAVersion:    strings.TrimSpace(os.Getenv(upgradeworker.CPAVersionEnvironment)),
		PublicOrigin:  strings.TrimSpace(os.Getenv("CPAMP_PUBLIC_ORIGIN")),
		ImportRuntime: importRuntime,
		VerifyRuntime: runtimeClient,
	}
	if *restart {
		parentRestart, bindErr := upgradeworker.BindLauncherParentRestart(workerParentRestartDelay)
		if bindErr != nil {
			return bindErr
		}
		defer parentRestart.Close()
		options.ValidateRestart = parentRestart.Validate
		options.RestartParent = parentRestart.Restart
	}
	worker, err := upgradeworker.New(ctx, options)
	if err != nil {
		return err
	}
	worker.SetHandler(&upgradewizard.Handler{
		LegacyRoot:                  func() string { return source },
		Database:                    func() string { return cfg.DBPath },
		DataKey:                     func() string { return cfg.DataKeyPath },
		PublicOrigin:                options.PublicOrigin,
		Authenticate:                worker.Authenticate,
		Execution:                   worker.Execution,
		ReconciliationState:         worker.ReconciliationState,
		ReconciliationDays:          worker.ReconciliationDays,
		ReconciliationHasParityGaps: worker.ReconciliationHasParityGaps,
		Runtime:                     runtimeClient,
		Artifact:                    artifactClient,
		PreserveManagerStore:        true,
	})
	err = upgradeworker.Serve(ctx, socket, worker)
	cancel()
	worker.Wait()
	return err
}
