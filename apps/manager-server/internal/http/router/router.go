package router

import (
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	accountactioncontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/accountaction"
	apikeyaliascontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/apikeyalias"
	codexinspectioncontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/codexinspection"
	dashboardcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/dashboard"
	healthcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/health"
	managerconfigcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/managerconfig"
	modelpricecontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/modelprice"
	monitoringcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/monitoring"
	panelcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/panel"
	proxycontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/proxy"
	setupcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/setup"
	systemcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/system"
	usagecontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	proxysvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/proxy"
)

func New(appCtx *app.Context) http.Handler {
	healthHandler := &healthcontroller.Handler{ServiceID: appCtx.ServiceID}
	systemHandler := &systemcontroller.Handler{App: appCtx}
	setupHandler := &setupcontroller.Handler{App: appCtx}
	managerConfigHandler := &managerconfigcontroller.Handler{App: appCtx}
	usageHandler := &usagecontroller.Handler{App: appCtx}
	modelPriceHandler := &modelpricecontroller.Handler{App: appCtx}
	apiKeyAliasHandler := &apikeyaliascontroller.Handler{App: appCtx}
	accountActionHandler := &accountactioncontroller.Handler{App: appCtx}
	codexInspectionHandler := &codexinspectioncontroller.Handler{App: appCtx}
	dashboardHandler := &dashboardcontroller.Handler{App: appCtx}
	monitoringHandler := &monitoringcontroller.Handler{App: appCtx}
	proxyHandler := &proxycontroller.Handler{App: appCtx}
	panelHandler := &panelcontroller.Handler{App: appCtx}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", middleware.WithCORS(appCtx.Config, healthHandler.Health))
	mux.HandleFunc("/status", middleware.WithCORS(appCtx.Config, systemHandler.Status))
	mux.HandleFunc("/usage-service/info", middleware.WithCORS(appCtx.Config, systemHandler.Info))
	mux.HandleFunc("/usage-service/config", middleware.WithCORS(appCtx.Config, managerConfigHandler.Handle))
	mux.HandleFunc("/setup", middleware.WithCORS(appCtx.Config, setupHandler.Setup))
	mux.HandleFunc("/management.html", panelHandler.ManagementHTML)
	mux.HandleFunc("/", rootHandler(appCtx, usageHandler, modelPriceHandler, apiKeyAliasHandler, accountActionHandler, codexInspectionHandler, dashboardHandler, monitoringHandler, proxyHandler))

	return middleware.Recovery(middleware.RequestLogger(mux))
}

func rootHandler(
	appCtx *app.Context,
	usageHandler *usagecontroller.Handler,
	modelPriceHandler *modelpricecontroller.Handler,
	apiKeyAliasHandler *apikeyaliascontroller.Handler,
	accountActionHandler *accountactioncontroller.Handler,
	codexInspectionHandler *codexinspectioncontroller.Handler,
	dashboardHandler *dashboardcontroller.Handler,
	monitoringHandler *monitoringcontroller.Handler,
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
		if strings.HasPrefix(r.URL.Path, "/v0/management/account-action-candidates") {
			middleware.WithCORS(appCtx.Config, accountActionHandler.Handle)(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v0/management/codex-inspection") {
			middleware.WithCORS(appCtx.Config, codexInspectionHandler.Handle)(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v0/management/dashboard/") {
			middleware.WithCORS(appCtx.Config, dashboardHandler.Handle)(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v0/management/monitoring/") {
			middleware.WithCORS(appCtx.Config, monitoringHandler.Handle)(w, r)
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
		if proxysvc.IsCPAPluginResourcePath(r.URL.Path) {
			middleware.WithCORS(appCtx.Config, proxyHandler.CPAResource)(w, r)
			return
		}
		if proxysvc.IsCPAProxyPath(r.URL.Path) {
			middleware.WithCORS(appCtx.Config, proxyHandler.CPA)(w, r)
			return
		}
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/management.html", http.StatusTemporaryRedirect)
			return
		}
		http.NotFound(w, r)
	}
}
