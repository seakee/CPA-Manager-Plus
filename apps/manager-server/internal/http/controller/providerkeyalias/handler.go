package providerkeyalias

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	providerkeyaliassvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/providerkeyalias"
)

type Handler struct {
	App *app.Context
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}

	path := strings.TrimRight(r.URL.Path, "/")
	const basePath = "/v0/management/provider-key-aliases"
	switch {
	case path == basePath && r.Method == http.MethodGet:
		aliases, err := h.App.ProviderKeyAliasService.List(r.Context())
		if err != nil {
			response.Error(w, http.StatusInternalServerError, err)
			return
		}
		response.JSON(w, http.StatusOK, map[string]any{"items": aliases})
	case path == basePath && r.Method == http.MethodPut:
		var req providerkeyaliassvc.SaveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		aliases, err := h.App.ProviderKeyAliasService.Save(r.Context(), req)
		if err != nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		response.JSON(w, http.StatusOK, map[string]any{"items": aliases})
	case strings.HasPrefix(path, basePath+"/") && r.Method == http.MethodDelete:
		parts := strings.Split(strings.TrimPrefix(path, basePath+"/"), "/")
		if len(parts) != 2 {
			response.MethodNotAllowed(w)
			return
		}
		if err := h.App.ProviderKeyAliasService.Delete(r.Context(), parts[0], parts[1]); err != nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		response.JSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		response.MethodNotAllowed(w)
	}
}
