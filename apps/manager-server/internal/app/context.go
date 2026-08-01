package app

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	accountactionsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/accountaction"
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	apikeyaliassvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/apikeyalias"
	automationsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/automation"
	bootstrapsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrap"
	codexinspectionsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/codexinspection"
	collectorsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	dashboardsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/dashboard"
	managerconfigsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	modelpricesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/modelprice"
	monitoringsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/monitoring"
	panelsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/panel"
	proxysvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/proxy"
	setupsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/setup"
	usagesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type AutomationRuntimeService interface {
	Reload(ctx context.Context) error
}

type Context struct {
	Config    config.Config
	Store     *store.Store
	Collector *collector.Manager

	StartedAt int64
	ServiceID string
	Bootstrap bootstrapsvc.Result

	SetupService                   *setupsvc.Service
	AdminAuthService               *adminauthsvc.Service
	ManagerConfigService           *managerconfigsvc.Service
	CollectorService               *collectorsvc.Service
	UsageService                   *usagesvc.Service
	DashboardService               *dashboardsvc.Service
	CodexInspectionService         *codexinspectionsvc.Service
	MonitoringService              *monitoringsvc.Service
	ModelPriceService              *modelpricesvc.Service
	APIKeyAliasService             *apikeyaliassvc.Service
	AccountActionService           *accountactionsvc.Service
	AccountProcessingPolicyService *automationsvc.Service
	AuthFileMutationCoordinator    *cpaauthfiles.MutationCoordinator
	ProxyService                   *proxysvc.Service
	PanelService                   *panelsvc.Service
	AutomationRuntimeService       AutomationRuntimeService
}

func FromExisting(
	cfg config.Config,
	st *store.Store,
	collectorManager *collector.Manager,
	startedAt int64,
	embeddedPanel fs.FS,
	modelPriceSyncURL *string,
	openRouterModelPriceSyncURL *string,
	serviceID string,
	automationRuntimeService ...AutomationRuntimeService,
) *Context {
	return fromExisting(
		cfg,
		st,
		collectorManager,
		startedAt,
		embeddedPanel,
		nil,
		modelPriceSyncURL,
		openRouterModelPriceSyncURL,
		serviceID,
		automationRuntimeService...,
	)
}

func FromExistingWithModelsDev(
	cfg config.Config,
	st *store.Store,
	collectorManager *collector.Manager,
	startedAt int64,
	embeddedPanel fs.FS,
	modelsDevModelPriceSyncURL *string,
	modelPriceSyncURL *string,
	openRouterModelPriceSyncURL *string,
	serviceID string,
	automationRuntimeService ...AutomationRuntimeService,
) *Context {
	return fromExisting(
		cfg,
		st,
		collectorManager,
		startedAt,
		embeddedPanel,
		modelsDevModelPriceSyncURL,
		modelPriceSyncURL,
		openRouterModelPriceSyncURL,
		serviceID,
		automationRuntimeService...,
	)
}

func fromExisting(
	cfg config.Config,
	st *store.Store,
	collectorManager *collector.Manager,
	startedAt int64,
	embeddedPanel fs.FS,
	modelsDevModelPriceSyncURL *string,
	modelPriceSyncURL *string,
	openRouterModelPriceSyncURL *string,
	serviceID string,
	automationRuntimeService ...AutomationRuntimeService,
) *Context {
	var runtimeService AutomationRuntimeService
	if len(automationRuntimeService) > 0 {
		runtimeService = automationRuntimeService[0]
	}
	collectorService := collectorsvc.New(collectorManager)
	managerConfigService := managerconfigsvc.New(cfg, st, collectorService)
	accountProcessingPolicyService := automationsvc.New(cfg, st)
	usageImportBaseDir := strings.TrimSpace(cfg.DataDir)
	if usageImportBaseDir == "" {
		usageImportBaseDir = filepath.Dir(cfg.DBPath)
	}
	usageService := usagesvc.New(st, usagesvc.WithImportSessions(usagesvc.ImportSessionConfig{
		Directory:      filepath.Join(usageImportBaseDir, "usage-imports"),
		ChunkSizeBytes: cfg.UsageImportChunkBytes,
		DiskQuotaBytes: cfg.UsageImportDiskQuotaBytes,
		MaxSessions:    cfg.UsageImportMaxSessions,
		TTL:            cfg.UsageImportSessionTTL,
	}))
	authFileMutationCoordinator := cpaauthfiles.NewMutationCoordinator()
	return &Context{
		Config:               cfg,
		Store:                st,
		Collector:            collectorManager,
		StartedAt:            startedAt,
		ServiceID:            serviceID,
		AdminAuthService:     adminauthsvc.New(cfg, st),
		SetupService:         setupsvc.New(cfg, st, collectorService, managerConfigService, startedAt, serviceID),
		ManagerConfigService: managerConfigService,
		CollectorService:     collectorService,
		UsageService:         usageService,
		DashboardService:     dashboardsvc.New(st, cfg.DashboardHourlyRollupEnabled),
		CodexInspectionService: codexinspectionsvc.NewWithOptions(
			st,
			managerConfigService,
			codexinspectionsvc.ServiceOptions{AuthFileMutationCoordinator: authFileMutationCoordinator},
		),
		MonitoringService:  monitoringsvc.New(st, cfg.DashboardHourlyRollupEnabled),
		ModelPriceService:  modelpricesvc.NewMultiSourceWithModelsDev(st, modelsDevModelPriceSyncURL, modelPriceSyncURL, openRouterModelPriceSyncURL, managerConfigService),
		APIKeyAliasService: apikeyaliassvc.New(st),
		AccountActionService: accountactionsvc.NewWithMutationCoordinator(
			st,
			managerConfigService,
			authFileMutationCoordinator,
		),
		AccountProcessingPolicyService: accountProcessingPolicyService,
		AuthFileMutationCoordinator:    authFileMutationCoordinator,
		ProxyService: proxysvc.NewWithMutationCoordinator(
			managerConfigService,
			authFileMutationCoordinator,
			st,
		),
		PanelService:             panelsvc.New(cfg.PanelPath, embeddedPanel),
		AutomationRuntimeService: runtimeService,
	}
}
