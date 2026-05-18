package app

import (
	"io/fs"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/collector"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/config"
	apikeyaliassvc "github.com/seakee/cpa-manager-plus/usage-service/internal/service/apikeyalias"
	collectorsvc "github.com/seakee/cpa-manager-plus/usage-service/internal/service/collector"
	managerconfigsvc "github.com/seakee/cpa-manager-plus/usage-service/internal/service/managerconfig"
	modelpricesvc "github.com/seakee/cpa-manager-plus/usage-service/internal/service/modelprice"
	panelsvc "github.com/seakee/cpa-manager-plus/usage-service/internal/service/panel"
	proxysvc "github.com/seakee/cpa-manager-plus/usage-service/internal/service/proxy"
	setupsvc "github.com/seakee/cpa-manager-plus/usage-service/internal/service/setup"
	usagesvc "github.com/seakee/cpa-manager-plus/usage-service/internal/service/usage"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/store"
)

type Context struct {
	Config    config.Config
	Store     *store.Store
	Collector *collector.Manager

	StartedAt int64
	ServiceID string

	SetupService         *setupsvc.Service
	ManagerConfigService *managerconfigsvc.Service
	CollectorService     *collectorsvc.Service
	UsageService         *usagesvc.Service
	ModelPriceService    *modelpricesvc.Service
	APIKeyAliasService   *apikeyaliassvc.Service
	ProxyService         *proxysvc.Service
	PanelService         *panelsvc.Service
}

func FromExisting(
	cfg config.Config,
	st *store.Store,
	collectorManager *collector.Manager,
	startedAt int64,
	embeddedPanel fs.FS,
	modelPriceSyncURL *string,
	serviceID string,
) *Context {
	collectorService := collectorsvc.New(collectorManager)
	managerConfigService := managerconfigsvc.New(cfg, st, collectorService)
	return &Context{
		Config:               cfg,
		Store:                st,
		Collector:            collectorManager,
		StartedAt:            startedAt,
		ServiceID:            serviceID,
		SetupService:         setupsvc.New(cfg, st, collectorService, managerConfigService, startedAt, serviceID),
		ManagerConfigService: managerConfigService,
		CollectorService:     collectorService,
		UsageService:         usagesvc.New(st),
		ModelPriceService:    modelpricesvc.New(st, modelPriceSyncURL),
		APIKeyAliasService:   apikeyaliassvc.New(st),
		ProxyService:         proxysvc.New(managerConfigService),
		PanelService:         panelsvc.New(cfg.PanelPath, embeddedPanel),
	}
}
