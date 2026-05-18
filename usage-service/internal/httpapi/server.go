package httpapi

import (
	"embed"
	"net/http"
	"time"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/app"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/collector"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/config"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/http/router"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/store"
)

//go:embed web/management.html
var embeddedPanel embed.FS

const serviceID = "cpa-manager-plus"

var modelPriceSyncURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

type Server struct {
	handler http.Handler
}

func New(cfg config.Config, store *store.Store, collector *collector.Manager) *Server {
	startedAt := time.Now().UnixMilli()
	appCtx := app.FromExisting(
		cfg,
		store,
		collector,
		startedAt,
		embeddedPanel,
		&modelPriceSyncURL,
		serviceID,
	)
	return &Server{
		handler: router.NewGin(appCtx),
	}
}

func (s *Server) Handler() http.Handler {
	return s.handler
}
