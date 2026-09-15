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

const (
	serviceID                   = "cpa-manager-plus"
	modelsDevModelPriceSyncURL  = "https://models.dev/catalog.json"
	modelPriceSyncURL           = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
	openRouterModelPriceSyncURL = "https://openrouter.ai/api/v1/models"
)

type modelPriceSyncURLs struct {
	modelsDev  string
	liteLLM    string
	openRouter string
}

type Server struct {
	handler http.Handler
	appCtx  *app.Context
}

func New(cfg config.Config, store *store.Store, collector *collector.Manager, automationRuntimeService ...app.AutomationRuntimeService) *Server {
	return newWithModelPriceSyncURLs(
		cfg,
		store,
		collector,
		modelPriceSyncURLs{
			modelsDev:  modelsDevModelPriceSyncURL,
			liteLLM:    modelPriceSyncURL,
			openRouter: openRouterModelPriceSyncURL,
		},
		true,
		automationRuntimeService...,
	)
}

func newWithModelPriceSyncURLs(
	cfg config.Config,
	store *store.Store,
	collector *collector.Manager,
	syncURLs modelPriceSyncURLs,
	enforceTrustedSourceURLs bool,
	automationRuntimeService ...app.AutomationRuntimeService,
) *Server {
	startedAt := time.Now().UnixMilli()
	var appCtx *app.Context
	if enforceTrustedSourceURLs {
		appCtx = app.FromExistingWithTrustedModelsDev(
			cfg,
			store,
			collector,
			startedAt,
			embeddedPanel,
			&syncURLs.modelsDev,
			&syncURLs.liteLLM,
			&syncURLs.openRouter,
			serviceID,
			automationRuntimeService...,
		)
	} else {
		appCtx = app.FromExistingWithModelsDev(
			cfg,
			store,
			collector,
			startedAt,
			embeddedPanel,
			&syncURLs.modelsDev,
			&syncURLs.liteLLM,
			&syncURLs.openRouter,
			serviceID,
			automationRuntimeService...,
		)
	}
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
