package app

import (
	"context"
	"io/fs"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	accountactionsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/accountaction"
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	apikeyaliassvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/apikeyalias"
	automationsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/automation"
	bootstrapsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrap"
	codexinspectionsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/codexinspection"
	collectorsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
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
	var runtimeService AutomationRuntimeService
	if len(automationRuntimeService) > 0 {
		runtimeService = automationRuntimeService[0]
	}
	collectorService := collectorsvc.New(collectorManager)
	managerConfigService := managerconfigsvc.New(cfg, st, collectorService)
	accountProcessingPolicyService := automationsvc.New(cfg, st)
	return &Context{
		Config:                         cfg,
		Store:                          st,
		Collector:                      collectorManager,
		StartedAt:                      startedAt,
		ServiceID:                      serviceID,
		AdminAuthService:               adminauthsvc.New(cfg, st),
		SetupService:                   setupsvc.New(cfg, st, collectorService, managerConfigService, startedAt, serviceID),
		ManagerConfigService:           managerConfigService,
		CollectorService:               collectorService,
		UsageService:                   usagesvc.New(st),
		DashboardService:               dashboardsvc.New(st),
		CodexInspectionService:         codexinspectionsvc.New(st, managerConfigService),
		MonitoringService:              monitoringsvc.New(st),
		ModelPriceService:              modelpricesvc.NewMultiSource(st, modelPriceSyncURL, openRouterModelPriceSyncURL, managerConfigService),
		APIKeyAliasService:             apikeyaliassvc.New(st),
		AccountActionService:           accountactionsvc.New(st, managerConfigService),
		AccountProcessingPolicyService: accountProcessingPolicyService,
		ProxyService:                   proxysvc.New(managerConfigService),
		PanelService:                   panelsvc.New(cfg.PanelPath, embeddedPanel),
		AutomationRuntimeService:       runtimeService,
	}
}
