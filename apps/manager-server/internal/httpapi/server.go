package httpapi

import (
	"embed"
	"net/http"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/router"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

//go:embed web/management.html
var embeddedPanel embed.FS

const serviceID = "cpa-manager-plus"

var modelsDevModelPriceSyncURL = "https://models.dev/catalog.json"
var modelPriceSyncURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
var openRouterModelPriceSyncURL = "https://openrouter.ai/api/v1/models"

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
		handler: router.New(appCtx),
		appCtx:  appCtx,
	}
}

func (s *Server) Handler() http.Handler {
	return s.handler
}

func (s *Server) AppContext() *app.Context {
	return s.appCtx
}
