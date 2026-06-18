package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/adminreset"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	bootstrapservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrap"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/worker"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "reset-admin-key", "reset-admin-password":
			if err := adminreset.Run(context.Background(), os.Args[2:], os.Stdout, os.Stderr); err != nil {
				log.Printf("reset admin key: %v", err)
				os.Exit(1)
			}
			return
		}
	}
	runServer()
}

func runServer() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	dataKey, dataKeyCreated, err := security.LoadOrCreateDataKey(cfg.DataKey, cfg.DataKeyPath)
	if err != nil {
		log.Fatalf("load data key: %v", err)
	}
	protector, err := security.NewProtector(dataKey)
	if err != nil {
		log.Fatalf("initialize secret protector: %v", err)
	}
	db, err := store.Open(cfg.DBPath, protector)
	if err != nil {
		log.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	bootstrapResult, err := bootstrapservice.Run(context.Background(), cfg, db, dataKeyCreated)
	if err != nil {
		log.Fatalf("bootstrap manager server: %v", err)
	}
	if bootstrapResult.GeneratedAdminKey != "" {
		log.Printf("CPA Manager Plus admin key generated: %s", bootstrapResult.GeneratedAdminKey)
	} else {
		log.Printf("CPA Manager Plus admin credential initialized")
	}
	if bootstrapResult.DataKeyCreated {
		log.Printf("CPA Manager Plus data key created at %s", cfg.DataKeyPath)
	}
	if bootstrapResult.MigratedLegacy {
		log.Printf("CPA Manager Plus legacy data migrated")
	}

	manager := collector.NewManager(cfg, db)
	collectorService := collectorservice.New(manager)
	collectorWorker := worker.NewCollectorWorker(cfg, db, collectorService)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverApp := httpapi.New(cfg, db, manager)
	automationSettingsService := serverApp.AppContext().AccountProcessingPolicyService
	runtimeSettings := automationSettingsService.RuntimeSettings(ctx)
	rateLimitAutoDisableWorker := worker.NewRateLimitAutoDisableWorker(db, collector.RuntimeConfig{
		CPAUpstreamURL: cfg.CPAUpstreamURL,
		ManagementKey:  cfg.ManagementKey,
	})
	accountActionWorker := worker.NewAccountActionCandidateWorker(db, runtimeSettings.AccountActionsAutoDisable)
	automationRuntime := worker.NewAutomationRuntime(
		automationSettingsService,
		manager,
		rateLimitAutoDisableWorker,
		accountActionWorker,
	)
	serverApp.AppContext().AutomationRuntimeService = automationRuntime
	automationRuntime.Start(ctx)

	collectorWorker.Start(ctx)

	codexInspectionWorker := worker.NewCodexInspectionWorker(serverApp.AppContext().Store, serverApp.AppContext().CodexInspectionService)
	codexInspectionWorker.Start(ctx)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           serverApp.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("cpa-manager-plus listening on %s", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	collectorWorker.Stop(context.Background())
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
