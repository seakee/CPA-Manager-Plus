package pluginquota

import (
	"errors"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	pluginquotaclient "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/pluginquota"
	pluginquotasvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/pluginquota"
)

// Handler exposes the plugin quota catalogue and the per-credential readings
// the panel renders. Every route speaks CPA's auth index, never a plugin
// specific credential identifier.
type Handler struct {
	App *app.Context
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	service := h.App.PluginQuotaService
	if service == nil {
		response.Error(w, http.StatusServiceUnavailable, pluginquotasvc.ErrNotConfigured)
		return
	}

	path := strings.TrimRight(r.URL.Path, "/")
	switch path {
	case "/v0/management/plugin-quota/providers":
		if r.Method != http.MethodGet {
			response.MethodNotAllowed(w)
			return
		}
		providers, err := service.Providers(r.Context())
		if err != nil {
			response.Error(w, pluginQuotaStatus(err), err)
			return
		}
		response.JSON(w, http.StatusOK, map[string]any{
			"providers": providers,
			"status":    service.Snapshot(),
		})
	case "/v0/management/plugin-quota/credential":
		if r.Method != http.MethodGet {
			response.MethodNotAllowed(w)
			return
		}
		authIndex := strings.TrimSpace(r.URL.Query().Get("auth_index"))
		quota, err := service.CredentialQuota(r.Context(), authIndex)
		if err != nil {
			response.Error(w, pluginQuotaStatus(err), err)
			return
		}
		response.JSON(w, http.StatusOK, quota)
	case "/v0/management/plugin-quota/refresh":
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		result, err := service.RefreshUpstream(r.Context(), strings.TrimSpace(r.URL.Query().Get("auth_index")))
		if err != nil {
			response.Error(w, pluginQuotaStatus(err), err)
			return
		}
		response.JSON(w, http.StatusOK, result)
	default:
		response.MethodNotAllowed(w)
	}
}

func pluginQuotaStatus(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, pluginquotasvc.ErrNotConfigured):
		return http.StatusServiceUnavailable
	case errors.Is(err, pluginquotasvc.ErrRefreshActive):
		return http.StatusConflict
	case errors.Is(err, pluginquotasvc.ErrCredentialMissing),
		errors.Is(err, pluginquotasvc.ErrNoQuotaProvider):
		return http.StatusNotFound
	case errors.Is(err, pluginquotaclient.ErrAuthIndexRequired):
		return http.StatusBadRequest
	default:
		return http.StatusBadGateway
	}
}
