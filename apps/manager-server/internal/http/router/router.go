package router

import (
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	routingdomain "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/routing"
	accountactioncontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/accountaction"
	apikeyaliascontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/apikeyalias"
	automationcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/automation"
	codexinspectioncontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/codexinspection"
	cpaupdatecontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/cpaupdate"
	dashboardcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/dashboard"
	healthcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/health"
	managerconfigcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/managerconfig"
	modelpricecontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/modelprice"
	monitoringcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/monitoring"
	panelcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/panel"
	proxycontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/proxy"
	quotacooldowncontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/quotacooldown"
	quotasnapshotcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/quotasnapshot"
	setupcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/setup"
	systemcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/system"
	updatecheckcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/updatecheck"
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
	automationHandler := automationcontroller.New(appCtx)
	quotaCooldownHandler := &quotacooldowncontroller.Handler{App: appCtx}
	codexInspectionHandler := &codexinspectioncontroller.Handler{App: appCtx}
	dashboardHandler := &dashboardcontroller.Handler{App: appCtx}
	monitoringHandler := &monitoringcontroller.Handler{App: appCtx}
	quotaSnapshotHandler := &quotasnapshotcontroller.Handler{App: appCtx}
	proxyHandler := &proxycontroller.Handler{App: appCtx}
	panelHandler := &panelcontroller.Handler{App: appCtx}

	mux := http.NewServeMux()
	updates := &updatecheckcontroller.Handler{App: appCtx}
	cpaUpdates := &cpaupdatecontroller.Handler{App: appCtx}
	mux.HandleFunc("/usage-service/updates", middleware.WithCORS(appCtx.Config, updates.Handle))
	mux.HandleFunc("/usage-service/updates/", middleware.WithCORS(appCtx.Config, updates.Handle))
	mux.HandleFunc("/usage-service/runtime/updates", middleware.WithCORS(appCtx.Config, cpaUpdates.Handle))
	mux.HandleFunc("/usage-service/runtime/updates/", middleware.WithCORS(appCtx.Config, cpaUpdates.Handle))
	mux.HandleFunc("/health", middleware.WithCORS(appCtx.Config, healthHandler.Health))
	// The legacy application is selected only after startup admission and
	// initialization. Fresh and pending startup use separate handlers and must
	// never emit this explicit legacy-mode acknowledgement. Public Ingress
	// denies this namespace; it is not a Gateway-safety qualification endpoint.
	mux.HandleFunc("/internal/routing/fresh-authority", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.EscapedPath() != r.URL.Path || r.URL.RawQuery != "" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/status", middleware.WithCORS(appCtx.Config, systemHandler.Status))
	mux.HandleFunc("/usage-service/info", middleware.WithCORS(appCtx.Config, systemHandler.Info))
	mux.HandleFunc("/usage-service/config", middleware.WithCORS(appCtx.Config, managerConfigHandler.Handle))
	mux.HandleFunc("/usage-service/account-processing-policy", middleware.WithCORS(appCtx.Config, automationHandler.Handle))
	mux.HandleFunc("/usage-service/quota-cooldowns", middleware.WithCORS(appCtx.Config, quotaCooldownHandler.Handle))
	mux.HandleFunc("/usage-service/quota-cooldowns/", middleware.WithCORS(appCtx.Config, quotaCooldownHandler.Handle))
	mux.HandleFunc("/setup", middleware.WithCORS(appCtx.Config, setupHandler.Setup))
	mux.HandleFunc("/management.html", panelHandler.ManagementHTML)
	mux.HandleFunc("/", rootHandler(appCtx, usageHandler, modelPriceHandler, apiKeyAliasHandler, accountActionHandler, codexInspectionHandler, dashboardHandler, monitoringHandler, quotaSnapshotHandler, managerConfigHandler, proxyHandler))

	return middleware.Recovery(middleware.RequestLogger(routingdomain.ProtectFixedDiscovery(mux, routingdomain.DiscoveryHandler{})))
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
	quotaSnapshotHandler *quotasnapshotcontroller.Handler,
	managerConfigHandler *managerconfigcontroller.Handler,
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
		cleanQuotaSnapshotPath := strings.TrimRight(r.URL.Path, "/")
		if cleanQuotaSnapshotPath == "/v0/management/quota-snapshots" ||
			cleanQuotaSnapshotPath == "/v0/management/quota-snapshots/query" {
			middleware.WithCORS(appCtx.Config, quotaSnapshotHandler.Handle)(w, r)
			return
		}
		cleanUsagePath := strings.TrimRight(r.URL.Path, "/")
		if cleanUsagePath == "/v0/management/usage" || strings.HasPrefix(cleanUsagePath, "/v0/management/usage/") {
			middleware.WithCORS(appCtx.Config, usageHandler.Handle)(w, r)
			return
		}
		if r.URL.Path == "/v0/management/cpa-connection/validate" {
			middleware.WithCORS(appCtx.Config, managerConfigHandler.ValidateCPAConnection)(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v0/management/") {
			middleware.WithCORS(appCtx.Config, proxyHandler.Management)(w, r)
			return
		}
		if r.URL.Path == "/v1/models" || r.URL.Path == "/v1/models/" ||
			r.URL.Path == "/models" || r.URL.Path == "/models/" {
			middleware.WithCORS(appCtx.Config, proxyHandler.ModelList)(w, r)
			return
		}
		if proxysvc.IsCPAPluginResourcePath(r.URL.Path) {
			middleware.WithCORS(appCtx.Config, proxyHandler.CPAResource)(w, r)
			return
		}
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/management.html", http.StatusTemporaryRedirect)
			return
		}
		http.NotFound(w, r)
	}
}
