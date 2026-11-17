package httpapi

import (
	"embed"
	"net/http"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/requestbody"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/router"
	modelpricesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/modelprice"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

//go:embed all:web
var embeddedPanel embed.FS

const serviceID = "cpa-manager-plus"

var modelsDevModelPriceSyncURL = modelpricesvc.DefaultModelsDevSyncURL
var modelPriceSyncURL = modelpricesvc.DefaultLiteLLMSyncURL
var openRouterModelPriceSyncURL = modelpricesvc.DefaultOpenRouterSyncURL

type Server struct {
	handler http.Handler
	appCtx  *app.Context
}

func New(cfg config.Config, store *store.Store, collector *collector.Manager, automationRuntimeService ...app.AutomationRuntimeService) *Server {
	startedAt := time.Now().UnixMilli()
	appCtx := app.FromExistingWithModelsDev(
		cfg,
		store,
		collector,
		startedAt,
		embeddedPanel,
		&modelsDevModelPriceSyncURL,
		&modelPriceSyncURL,
		&openRouterModelPriceSyncURL,
		serviceID,
		automationRuntimeService...,
	)
	return &Server{
		handler: requestbody.Middleware(requestbody.DefaultReadIdleTimeout, ProtectBrandResources(router.New(appCtx))),
		appCtx:  appCtx,
	}
}

func (s *Server) Handler() http.Handler {
	return s.handler
}

func (s *Server) AppContext() *app.Context {
	return s.appCtx
}

func (s *Server) SetUpgradeAPIKeys(fn func(w http.ResponseWriter, r *http.Request, resource string)) {
	if s != nil && s.appCtx != nil {
		s.appCtx.UpgradeAPIKeys = fn
	}
}

func (s *Server) WrapHandler(wrap func(http.Handler) http.Handler) {
	if s != nil && wrap != nil {
		s.handler = wrap(s.handler)
	}
}
