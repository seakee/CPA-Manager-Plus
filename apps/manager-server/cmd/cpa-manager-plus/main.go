package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	httppprof "net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/cpaidentityinventory"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/credentialdeletemutation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identitymutation"
	identityprojectionapp "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityprojection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityreconcile"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/adminreset"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/cpaconnection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/derivedmaintenance"
	legacyartifactcommand "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/legacyartifact"
	legacypreflightcommand "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/legacypreflight"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/managerdatabundle"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/managerdatasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/runtimeconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/usagecompact"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	identitystoreports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	bootstrapservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	cpaupdateservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaupdate"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/worker"
)

func main() {
	if handled, err := writeVersion(os.Args[1:], os.Stdout); handled {
		if err != nil {
			log.Printf("write version: %v", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "initialize":
			if len(os.Args) == 3 && (os.Args[2] == "--help" || os.Args[2] == "-h") {
				fmt.Fprintln(os.Stdout, "Usage: cpa-manager-plus initialize\nAdmit the real Fresh Manager store and print one short-lived Bootstrap fragment handoff to this host caller, then exit. Existing Fresh operations resume without resetting Admin. Stop the writable service before reissue/resume; the initial read-only listener may remain running. No service-log credential delivery. Unknown/legacy sources remain blocked.")
				return
			}
			if len(os.Args) != 2 {
				log.Fatal("Usage: cpa-manager-plus initialize")
			}
			if err := runFreshInitialize(context.Background(), os.Stdout); err != nil {
				log.Print("Fresh initialization/handoff unavailable; inspect the host source and ownership before retrying")
				os.Exit(1)
			}
			return
		case "legacy-preflight":
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := legacypreflightcommand.Run(ctx, os.Args[2:], os.Stdout, os.Stderr); err != nil {
				if legacypreflightcommand.IsHelp(err) {
					return
				}
				log.Printf("legacy preflight: %v", err)
				os.Exit(1)
			}
			return
		case "legacy-writer-artifact":
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := legacyartifactcommand.Run(ctx, os.Args[2:], os.Stdout, os.Stderr); err != nil {
				if legacyartifactcommand.IsHelp(err) {
					return
				}
				log.Printf("verify legacy writer artifact: %v", err)
				os.Exit(1)
			}
			return
		case "reset-admin-key", "reset-admin-password":
			if err := adminreset.Run(context.Background(), os.Args[2:], os.Stdout, os.Stderr); err != nil {
				log.Printf("reset admin key: %v", err)
				os.Exit(1)
			}
			return
		case "cleanup-derived":
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := derivedmaintenance.Run(ctx, os.Args[2:], os.Stdout, os.Stderr); err != nil {
				log.Printf("cleanup derived data: %v", err)
				os.Exit(1)
			}
			return
		case "compact-usage":
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := usagecompact.Run(ctx, os.Args[2:], os.Stdout, os.Stderr); err != nil {
				if usagecompact.IsHelp(err) {
					return
				}
				log.Printf("compact usage database: %v", err)
				os.Exit(1)
			}
			return
		case "store-cpa-connection":
			if err := cpaconnection.Run(context.Background(), os.Args[2:], os.Stdout, os.Stderr); err != nil {
				log.Printf("store CPA connection: %v", err)
				os.Exit(1)
			}
			return
		case "manager-data-snapshot":
			if err := runManagerDataSnapshotCommand(os.Args[2:], os.Stdout, os.Stderr); err != nil {
				log.Printf("manage Manager data snapshot: %v", err)
				os.Exit(1)
			}
			return
		case "manager-data-bundle":
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := managerdatabundle.Run(ctx, os.Args[2:], os.Stdout, os.Stderr); err != nil {
				log.Printf("manage Manager data bundle: %v", err)
				os.Exit(1)
			}
			return
		case "sanitize-runtime-config":
			if err := runtimeconfig.Run(os.Args[2:], os.Stdout, os.Stderr); err != nil {
				log.Printf("sanitize runtime config: %v", err)
				os.Exit(1)
			}
			return
		}
	}
	runServer()
}

func startupPreflightTimeout() (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv("CPA_MANAGER_PREFLIGHT_TIMEOUT"))
	if value == "" {
		return 2 * time.Minute, nil
	}
	timeout, err := time.ParseDuration(value)
	if err != nil || timeout <= 0 {
		return 0, errors.New("CPA_MANAGER_PREFLIGHT_TIMEOUT must be a positive duration, for example 2m or 5m")
	}
	return timeout, nil
}

func writeVersion(args []string, stdout io.Writer) (bool, error) {
	if len(args) != 1 || (args[0] != "-v" && args[0] != "--version") {
		return false, nil
	}
	_, err := fmt.Fprintln(stdout, buildinfo.Version)
	return true, err
}

func runManagerDataSnapshotCommand(args []string, stdout io.Writer, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return managerdatasnapshot.Run(ctx, args, stdout, stderr)
}

func runServer() {
	runServerWithOptions(legacypreflight.Options{})
}

func runServerWithOptions(options legacypreflight.Options) {
	inspectionTimeout, err := startupPreflightTimeout()
	if err != nil {
		log.Fatalf("configure startup inspection: %v", err)
	}
	cfg, err := config.LoadForInspection()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	configPath, err := config.InspectionConfigPath()
	if err != nil {
		log.Fatalf("resolve inspection config: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	paths := sqliterepo.InspectionPaths{
		Database: cfg.DBPath, Config: configPath, DataKey: cfg.DataKeyPath, Archives: cfg.UsageArchiveDir,
	}
	// The listener comes up before product/data mutation. Discovery starts
	// unavailable, then switches only to current read-only evidence: missing-DB
	// inventory for Fresh/partial sources, or a bounded rollback-mode SQLite
	// read snapshot for an existing database.
	startupHandler := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		log.Fatalf("http listen: %v", err)
	}
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           startupHandler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	serverResult := make(chan error, 1)
	log.Printf("cpa-manager-plus listening on %s", listener.Addr())
	go serveHTTPServer(server, listener, stop, serverResult)
	// A Fresh store can only have been admitted by the bounded host command.
	// Retain that ownership through this service incarnation and bypass every
	// historical bootstrap/setup/collector/lifecycle side effect.
	if freshMarkerPresent(paths.Database) {
		opened, openErr := serveExistingFresh(ctx, cfg, paths, startupHandler)
		if openErr == nil {
			defer opened.Close()
			waitForReadOnlyStartup(ctx, server, serverResult)
			return
		}
		// A competing host initializer may still hold this exact admission.
		// Keep discovery unavailable and retry ownership validation after it
		// releases the fence; never project this Fresh store as a legacy source.
		go awaitFreshStore(ctx, cfg, paths, startupHandler)
		waitForReadOnlyStartup(ctx, server, serverResult)
		return
	}

	if !options.Initialize {
		if _, statErr := os.Lstat(paths.Database); os.IsNotExist(statErr) {
			startupHandler.EnableReadOnlyDiscovery(paths, options, inspectionTimeout)
			go awaitFreshStore(ctx, cfg, paths, startupHandler)
			log.Printf("Manager source has no database; serving read-only Bootstrap discovery pending a later authorized initializer")
			waitForReadOnlyStartup(ctx, server, serverResult)
			return
		}
	}

	inspectionCtx, cancelInspection := context.WithTimeout(ctx, inspectionTimeout)
	databaseLock, err := legacypreflight.AdmitStartup(inspectionCtx, paths, options)
	cancelInspection()
	if err != nil {
		log.Printf("Manager writable startup is not authorized; keeping read-only discovery available: %v", err)
		startupHandler.EnableReadOnlyDiscovery(paths, options, inspectionTimeout)
		waitForReadOnlyStartup(ctx, server, serverResult)
		return
	}
	defer func() {
		if err := databaseLock.Close(); err != nil {
			log.Printf("close manager database process lock: %v", err)
		}
	}()
	// Admission permits writes, not a Bootstrap Completed transition. Revoke
	// the pre-write observation before config/key/store initialization can make
	// it stale. Phase4-01 has no qualified post-write discovery authority.
	startupHandler.BeginWritableStartup()
	// Defaults are created only after the source has passed fenced admission.
	// Fatalf exits without running defers; release/rollback admission explicitly
	// on initialization errors before a database has been opened successfully.
	failStartup := func(format string, args ...any) {
		if err := databaseLock.Close(); err != nil {
			log.Printf("close failed startup admission: %v", err)
		}
		log.Fatalf(format, args...)
	}
	loadedConfig, err := config.Load()
	if err != nil {
		failStartup("load admitted config: %v", err)
	}
	if loadedConfig.DBPath != cfg.DBPath || loadedConfig.DataKeyPath != cfg.DataKeyPath || loadedConfig.UsageArchiveDir != cfg.UsageArchiveDir || loadedConfig.DataKey != cfg.DataKey {
		failStartup("source configuration changed before startup")
	}
	cfg = loadedConfig
	cfg.DBPath = databaseLock.DatabasePath()
	if err := sqliterepo.RequireDataKeyForInspectedConnection(
		databaseLock.ConnectionStorage,
		cfg.DataKey,
		cfg.DataKeyPath,
	); err != nil {
		failStartup("validate data key availability: %v", err)
	}
	dataKey, dataKeyCreated, err := security.LoadOrCreateDataKey(cfg.DataKey, cfg.DataKeyPath)
	if err != nil {
		failStartup("load data key: %v", err)
	}
	protector, err := security.NewProtector(dataKey)
	if err != nil {
		failStartup("initialize secret protector: %v", err)
	}
	db, err := store.Open(cfg.DBPath, protector)
	if err != nil {
		failStartup("open sqlite: %v", err)
	}
	databaseLock.RetainMarker()
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("close sqlite: %v", err)
		}
	}()

	bootstrapResult, err := bootstrapservice.Run(context.Background(), cfg, db, dataKeyCreated)
	if err != nil {
		log.Fatalf("bootstrap manager server: %v", err)
	}
	log.Printf("CPA Manager Plus administrator authority loaded")
	if bootstrapResult.DataKeyCreated {
		log.Printf("CPA Manager Plus data key created at %s", cfg.DataKeyPath)
	}
	if bootstrapResult.MigratedLegacy {
		log.Printf("CPA Manager Plus legacy data migrated")
	}

	manager := collector.NewManager(cfg, db)
	collectorService := collectorservice.New(manager)
	collectorWorker := worker.NewCollectorWorker(cfg, db, collectorService)
	walMaintenance, err := sqliterepo.NewWALMaintenance(cfg.DBPath)
	if err != nil {
		log.Printf("configure SQLite WAL maintenance: %v", err)
	} else {
		walMaintenance.Start(ctx)
		defer func() {
			if err := walMaintenance.Close(); err != nil {
				log.Printf("close SQLite WAL maintenance: %v", err)
			}
		}()
	}

	serverApp := httpapi.New(cfg, db, manager)
	serverApp.AppContext().DatabaseMaintenance = walMaintenance
	runtimeMode := model.RuntimeModeExternal
	var runtimeClient runtimeservice.RuntimeClient
	if cfg.EmbeddedRuntimeConfigured() {
		runtimeMode = model.RuntimeModeEmbedded
		runtimeClient = runtimeservice.NewEmbeddedClientWithTokenSource(
			cfg.RuntimeURL,
			runtimeservice.NewFileRuntimeTokenSource(cfg.RuntimeTokenFile),
		)
	} else {
		managerConfigService := serverApp.AppContext().ManagerConfigService
		runtimeClient = runtimeservice.NewExternalClientWithConnectionSource(
			func(statusCtx context.Context) (string, string, error) {
				externalConfig, _, _, resolveErr := managerConfigService.ResolveManagerConfigWithSource(statusCtx)
				if resolveErr != nil {
					return "", "", resolveErr
				}
				return externalConfig.CPAConnection.CPABaseURL, externalConfig.CPAConnection.ManagementKey, nil
			},
		)
	}
	serverApp.AppContext().CPAUpdateService = cpaupdateservice.New(db, runtimeClient, runtimeMode)
	recoveryCtx, cancelRecovery := context.WithTimeout(context.Background(), 10*time.Second)
	if err := serverApp.AppContext().CodexInspectionService.Recover(recoveryCtx); err != nil {
		log.Printf("recover codex inspection runs: %v", err)
	}
	cancelRecovery()
	automationSettingsService := serverApp.AppContext().AccountProcessingPolicyService
	runtimeSettings := automationSettingsService.RuntimeSettings(ctx)
	rateLimitAutoDisableWorker := worker.NewRateLimitAutoDisableWorkerWithMutationCoordinator(
		db,
		serverApp.AppContext().AuthFileMutationCoordinator,
		collector.RuntimeConfig{
			CPAUpstreamURL: cfg.CPAUpstreamURL,
			ManagementKey:  cfg.ManagementKey,
		},
	)
	serverApp.AppContext().QuotaCooldownRecoveryService = rateLimitAutoDisableWorker
	accountActionWorker := worker.NewAccountActionCandidateWorkerWithMutationCoordinator(
		db,
		serverApp.AppContext().AuthFileMutationCoordinator,
		runtimeSettings.AccountActionsAutoDisable,
	)
	accountHistoryRollupWorker := worker.NewAccountHistoryRollupWorker(db)
	usageDerivedRollupWorker := worker.NewUsagePricingRollupWorker(db)
	serverApp.AppContext().ModelPriceService.SetPricesChangedNotifier(usageDerivedRollupWorker.Wake)
	var usageHourlyAggregateWorker *worker.UsageHourlyAggregateWorker
	if cfg.DashboardHourlyRollupEnabled {
		usageHourlyAggregateWorker = worker.NewUsageHourlyAggregateWorker(db)
	}
	identityProjectionWorker := identityprojectionapp.NewWorker(db.IdentityProjections)
	var usageArchiveRetentionWorker *worker.UsageArchiveRetentionWorker
	if cfg.UsageArchiveRetentionEnabled && cfg.UsageArchiveRetentionDays > 0 && cfg.DashboardHourlyRollupEnabled {
		usageArchiveRetentionWorker = worker.NewUsageArchiveRetentionWorker(
			serverApp.AppContext().UsageService,
			cfg.UsageArchiveRetentionDays,
		)
	}

	serverApp.AppContext().UsageService.SetEventsInsertedNotifier(func() {
		accountHistoryRollupWorker.Wake()
		usageDerivedRollupWorker.Wake()
		if usageHourlyAggregateWorker != nil {
			usageHourlyAggregateWorker.Wake()
		}
		identityProjectionWorker.Wake()
	})
	automationRuntime := worker.NewAutomationRuntime(
		automationSettingsService,
		manager,
		rateLimitAutoDisableWorker,
		accountActionWorker,
	)
	serverApp.AppContext().AutomationRuntimeService = automationRuntime
	manager.SetUsageEventHandler(worker.NewUsageEventFanout(
		automationRuntime.UsageEventHandler(),
		accountHistoryRollupWorker,
		usageDerivedRollupWorker,
		usageHourlyAggregateWorker,
		collector.UsageEventHandlerFunc(func(ctx context.Context, _ collector.RuntimeConfig, events []usage.Event) {
			identityProjectionWorker.HandleUsageEvents(ctx, events)
		}),
	))

	pprofServer, err := newPprofServer(cfg.PprofAddr)
	if err != nil {
		log.Fatalf("configure pprof: %v", err)
	}
	if pprofServer != nil {
		go func() {
			log.Printf("cpa-manager-plus pprof listening on %s", pprofServer.Addr)
			if err := pprofServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("pprof server: %v", err)
			}
		}()
	}

	codexInspectionWorker := worker.NewCodexInspectionWorker(serverApp.AppContext().Store, serverApp.AppContext().CodexInspectionService)
	var identityProcessInstanceID string
	var mutationSvc *identitymutation.Service
	var credentialDeleteSvc *credentialdeletemutation.Service
	identityClock := identitymutation.NewMonotonicMillis(nil)
	if cfg.EmbeddedRuntimeConfigured() {
		instanceID, err := identity.NewAPIKeyID()
		if err != nil {
			log.Fatalf("create identity process instance: %v", err)
		}
		identityProcessInstanceID = string(instanceID)
		mutationRepo, ok := db.Identities.(identitystoreports.MutationRepository)
		if !ok {
			log.Fatal("identity store does not support API-key mutation intents")
		}
		mutationSvc, err = identitymutation.NewService(identitymutation.Config{
			RuntimeObserver:   runtimeClient,
			InventoryClient:   cpaidentityinventory.New(nil, nil),
			Repository:        mutationRepo,
			ProcessInstanceID: identityProcessInstanceID,
			TimeSource:        identityClock,
		})
		if err != nil {
			log.Fatalf("initialize API-key mutation service: %v", err)
		}
		serverApp.AppContext().ProxyService.SetAPIKeyMutationService(mutationSvc)
		credentialRepo, ok := db.Identities.(identitystoreports.CredentialDeleteRepository)
		if !ok {
			log.Fatal("identity store does not support credential delete intents")
		}
		credentialDeleteSvc, err = credentialdeletemutation.NewService(credentialdeletemutation.Config{
			RuntimeObserver:   runtimeClient,
			Repository:        credentialRepo,
			ProcessInstanceID: identityProcessInstanceID,
			TimeSource:        identityClock,
		})
		if err != nil {
			log.Fatalf("initialize credential delete service: %v", err)
		}
		serverApp.AppContext().ProxyService.SetCredentialDeleteService(credentialDeleteSvc)
	}
	// Only after the complete writable Manager graph is assembled do unrelated
	// routes become reachable. The fixed discovery path remains captured by the
	// outer handler for its independent Bootstrap lifecycle. Graph readiness
	// grants no Completed authority; the invalidated projection stays unavailable.
	startupHandler.SetReady(serverApp.Handler())
	if cfg.EmbeddedRuntimeConfigured() {
		runtimeReconciler := runtimeservice.NewReconciler(
			runtimeClient,
			db,
			runtimeservice.DefaultReconcileInterval,
			log.Printf,
		)
		go runtimeReconciler.Run(ctx)

		managerConfigSvc := serverApp.AppContext().ManagerConfigService
		identityReconcileSvc, err := identityreconcile.NewService(identityreconcile.Config{
			RuntimeObserver: runtimeClient,
			ConnectionResolver: func(connCtx context.Context) (string, string, error) {
				setup, ok, err := managerConfigSvc.ResolveSetup(connCtx)
				if err != nil {
					return "", "", err
				}
				if !ok {
					return "", "", errors.New("CPA connection setup is not available")
				}
				return setup.CPAUpstreamURL, setup.ManagementKey, nil
			},
			InventoryClient:   cpaidentityinventory.New(nil, nil),
			IdentityRepo:      db.Identities,
			ProcessInstanceID: identityProcessInstanceID,
			TimeSource:        identityClock,
			BeforeCapture: func(captureCtx context.Context) error {
				return retryIdentityForwardCompletions(captureCtx,
					mutationSvc.RetryForwardCompletions, credentialDeleteSvc.RetryForwardCompletions)
			},
			CredentialDeletePhysicalObserver: func(captureCtx context.Context, baseURL, managementKey, runtimeIdentity string) (map[string]identitystoreports.PhysicalSourceEvidence, error) {
				pending, err := credentialDeleteSvc.PendingPhysicalSources(captureCtx, runtimeIdentity)
				if err != nil {
					return nil, err
				}
				evidence := make(map[string]identitystoreports.PhysicalSourceEvidence, len(pending))
				client := cpaauthfiles.New(nil)
				for _, item := range pending {
					present, probeErr := client.PhysicalFileExists(captureCtx, baseURL, managementKey, item.PhysicalName)
					if probeErr != nil {
						continue
					}
					if present {
						evidence[item.IntentID] = identitystoreports.PhysicalSourcePresent
					} else {
						evidence[item.IntentID] = identitystoreports.PhysicalSourceAbsent
					}
				}
				return evidence, nil
			},
		})
		if err != nil {
			log.Fatalf("initialize identity reconcile service: %v", err)
		}
		identityWorker := identityreconcile.NewWorker(identityReconcileSvc, log.Printf)
		go identityWorker.Run(ctx)
	}
	go serverApp.AppContext().UpdateCheckService.Run(ctx)
	if err := serverApp.AppContext().UsageService.StartImportSessionCleanup(ctx); err != nil {
		log.Printf("[startup] start usage import session cleanup: %v", err)
	}
	if err := serverApp.AppContext().UsageService.StartArchiveJobs(ctx); err != nil {
		log.Printf("[startup] start usage archive jobs: %v", err)
	}

	if err := db.RunDerivedStartupMaintenance(ctx); err != nil && ctx.Err() == nil {
		log.Printf("[startup] post-listen index preparation failed; continuing without blocking background workers: %v", err)
	}
	if ctx.Err() == nil {
		log.Printf("[startup] starting background workers")
		automationRuntime.Start(ctx)
		codexInspectionWorker.Start(ctx)
		accountHistoryRollupWorker.Start(ctx)
		usageDerivedRollupWorker.Start(ctx)
		if usageHourlyAggregateWorker != nil {
			usageHourlyAggregateWorker.Start(ctx)
		}
		identityProjectionWorker.Start(ctx)
		db.StartDerivedMaintenance(ctx)
		collectorWorker.Start(ctx)
		worker.NewLegacyQuotaSnapshotMigrationWorker(db).Start(ctx)
	}

	usageCacheAccountingMigrationWorker := worker.NewUsageCacheAccountingMigrationWorker(db, func() {
		accountHistoryRollupWorker.Wake()
		usageDerivedRollupWorker.Wake()
		if usageHourlyAggregateWorker != nil {
			usageHourlyAggregateWorker.Wake()
		}
		identityProjectionWorker.Wake()
		go func() {
			if err := waitForUsageResponseMetadataBackfill(ctx, db); err != nil {
				if ctx.Err() == nil {
					log.Printf("usage response metadata backfill: %v", err)
				}
				return
			}
			if usageArchiveRetentionWorker != nil {
				usageArchiveRetentionWorker.Start(ctx)
			}
		}()

	})
	if ctx.Err() == nil {
		usageCacheAccountingMigrationWorker.Start(ctx)
	}

	select {
	case <-ctx.Done():
		select {
		case err := <-serverResult:
			if err != nil {
				log.Printf("http server stopped unexpectedly: %v", err)
			}
		default:
		}
	case err := <-serverResult:
		if err != nil {
			log.Printf("http server stopped unexpectedly: %v", err)
		}
		// Ensure every worker using the shared process context observes the same
		// shutdown even when the HTTP listener, rather than a signal, initiated it.
		stop()
	}
	stopCodexInspectionWorker(codexInspectionWorker, 20*time.Second)
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	collectorWorker.Stop(context.Background())
	if pprofServer != nil {
		if err := pprofServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown pprof: %v", err)
		}
	}
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	if err := usageArchiveRetentionWorker.StopAndWait(shutdownCtx); err != nil {
		log.Printf("shutdown usage archive retention: %v", err)
	}
	if err := serverApp.AppContext().UsageService.WaitArchiveJobs(shutdownCtx); err != nil {
		log.Printf("shutdown usage archive jobs: %v", err)
	}
}

func retryIdentityForwardCompletions(ctx context.Context, apiKeyRetry, credentialRetry func(context.Context) error) error {
	apiKeyErr := apiKeyRetry(ctx)
	credentialErr := credentialRetry(ctx)
	return errors.Join(apiKeyErr, credentialErr)
}

func serveHTTPServer(server *http.Server, listener net.Listener, stop context.CancelFunc, result chan<- error) {
	err := server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	result <- err
	stop()
}

// waitForReadOnlyStartup keeps a safely bound discovery/liveness listener alive
// when the source is rejected by the normal writable gate. No Store, worker or
// mutation handler is constructed on this path.
func waitForReadOnlyStartup(ctx context.Context, server *http.Server, result <-chan error) {
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = server.Shutdown(shutdownCtx)
		cancel()
		return
	case err := <-result:
		if err != nil {
			log.Printf("read-only discovery server stopped: %v", err)
		}
	}
}

type codexInspectionStopper interface {
	StopAndWait(context.Context) error
}

func stopCodexInspectionWorker(stopper codexInspectionStopper, timeout time.Duration) {
	if stopper == nil {
		return
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), timeout)
	err := stopper.StopAndWait(shutdownCtx)
	cancelShutdown()
	if err == nil {
		return
	}
	// Shutdown must remain bounded. The worker has already fenced new starts and
	// cancelled owned work; if it still cannot finish within the grace period,
	// leave an explicit error for the supervisor and let startup recovery reclaim
	// the expired lease instead of hanging the process indefinitely.
	log.Printf("shutdown codex inspection worker: %v; continuing bounded process shutdown", err)
}

func newPprofServer(addr string) (*http.Server, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid address %q: %w", addr, err)
	}
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("pprof address must use a loopback host: %q", addr)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", httppprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", httppprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", httppprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", httppprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", httppprof.Trace)
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}, nil
}

const usageResponseMetadataBackfillRetryDelay = 5 * time.Second

func waitForUsageResponseMetadataBackfill(ctx context.Context, db *store.Store) error {
	return waitForBackfillReadiness(ctx, usageResponseMetadataBackfillRetryDelay, func(runCtx context.Context) error {
		return runUsageResponseMetadataBackfill(runCtx, db)
	})
}

func waitForBackfillReadiness(ctx context.Context, retryDelay time.Duration, run func(context.Context) error) error {
	if run == nil {
		return errors.New("usage response metadata backfill is not configured")
	}
	if retryDelay <= 0 {
		retryDelay = time.Second
	}
	for {
		err := run(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Printf("usage response metadata backfill failed; will retry: %v", err)
		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func runUsageResponseMetadataBackfill(ctx context.Context, db *store.Store) error {
	const batchLimit = 1000
	total := 0
	for {
		updated, err := db.BackfillUsageResponseMetadata(ctx, batchLimit)
		if err != nil {
			return err
		}
		if updated == 0 {
			if total > 0 {
				log.Printf("usage response metadata backfill completed: updated=%d", total)
			}
			return nil
		}
		total += updated
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
