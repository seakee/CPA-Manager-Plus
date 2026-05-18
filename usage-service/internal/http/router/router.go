package router

import (
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/app"
	apikeyaliascontroller "github.com/seakee/cpa-manager-plus/usage-service/internal/http/controller/apikeyalias"
	healthcontroller "github.com/seakee/cpa-manager-plus/usage-service/internal/http/controller/health"
	managerconfigcontroller "github.com/seakee/cpa-manager-plus/usage-service/internal/http/controller/managerconfig"
	modelpricecontroller "github.com/seakee/cpa-manager-plus/usage-service/internal/http/controller/modelprice"
	panelcontroller "github.com/seakee/cpa-manager-plus/usage-service/internal/http/controller/panel"
	proxycontroller "github.com/seakee/cpa-manager-plus/usage-service/internal/http/controller/proxy"
	setupcontroller "github.com/seakee/cpa-manager-plus/usage-service/internal/http/controller/setup"
	systemcontroller "github.com/seakee/cpa-manager-plus/usage-service/internal/http/controller/system"
	usagecontroller "github.com/seakee/cpa-manager-plus/usage-service/internal/http/controller/usage"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/http/middleware"
	proxysvc "github.com/seakee/cpa-manager-plus/usage-service/internal/service/proxy"
)

func New(appCtx *app.Context) http.Handler {
	healthHandler := &healthcontroller.Handler{ServiceID: appCtx.ServiceID}
	systemHandler := &systemcontroller.Handler{App: appCtx}
	setupHandler := &setupcontroller.Handler{App: appCtx}
	managerConfigHandler := &managerconfigcontroller.Handler{App: appCtx}
	usageHandler := &usagecontroller.Handler{App: appCtx}
	modelPriceHandler := &modelpricecontroller.Handler{App: appCtx}
	apiKeyAliasHandler := &apikeyaliascontroller.Handler{App: appCtx}
	proxyHandler := &proxycontroller.Handler{App: appCtx}
	panelHandler := &panelcontroller.Handler{App: appCtx}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", middleware.WithCORS(appCtx.Config, healthHandler.Health))
	mux.HandleFunc("/status", middleware.WithCORS(appCtx.Config, systemHandler.Status))
	mux.HandleFunc("/usage-service/info", middleware.WithCORS(appCtx.Config, systemHandler.Info))
	mux.HandleFunc("/usage-service/config", middleware.WithCORS(appCtx.Config, managerConfigHandler.Handle))
	mux.HandleFunc("/setup", middleware.WithCORS(appCtx.Config, setupHandler.Setup))
	mux.HandleFunc("/management.html", panelHandler.ManagementHTML)
	mux.HandleFunc("/", rootHandler(appCtx, usageHandler, modelPriceHandler, apiKeyAliasHandler, proxyHandler))

	return middleware.Recovery(middleware.RequestLogger(mux))
}

func rootHandler(
	appCtx *app.Context,
	usageHandler *usagecontroller.Handler,
	modelPriceHandler *modelpricecontroller.Handler,
	apiKeyAliasHandler *apikeyaliascontroller.Handler,
	proxyHandler *proxycontroller.Handler,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			middleware.WriteCORS(appCtx.Config, w, r)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v0/management/model-prices") {
			middleware.WithCORS(appCtx.Config, modelPriceHandler.Handle)(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v0/management/api-key-aliases") {
			middleware.WithCORS(appCtx.Config, apiKeyAliasHandler.Handle)(w, r)
			return
		}
		cleanUsagePath := strings.TrimRight(r.URL.Path, "/")
		if cleanUsagePath == "/v0/management/usage" || strings.HasPrefix(cleanUsagePath, "/v0/management/usage/") {
			middleware.WithCORS(appCtx.Config, usageHandler.Handle)(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v0/management/") {
			middleware.WithCORS(appCtx.Config, proxyHandler.Management)(w, r)
			return
		}
		if proxysvc.IsModelListPath(r.URL.Path) {
			middleware.WithCORS(appCtx.Config, proxyHandler.ModelList)(w, r)
			return
		}
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/management.html", http.StatusTemporaryRedirect)
			return
		}
		http.NotFound(w, r)
	}
}
