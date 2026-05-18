package apikeyalias

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/app"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/http/response"
	apikeyaliassvc "github.com/seakee/cpa-manager-plus/usage-service/internal/service/apikeyalias"
)

type Handler struct {
	App *app.Context
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizeIfConfigured(w, r, h.App.ManagerConfigService) {
		return
	}

	path := strings.TrimRight(r.URL.Path, "/")
	const basePath = "/v0/management/api-key-aliases"
	switch {
	case path == basePath && r.Method == http.MethodGet:
		aliases, err := h.App.APIKeyAliasService.List(r.Context())
		if err != nil {
			response.Error(w, http.StatusInternalServerError, err)
			return
		}
		response.JSON(w, http.StatusOK, map[string]any{"items": aliases})
	case path == basePath && r.Method == http.MethodPut:
		var req apikeyaliassvc.SaveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		aliases, err := h.App.APIKeyAliasService.Save(r.Context(), req.Items)
		if err != nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		response.JSON(w, http.StatusOK, map[string]any{"items": aliases})
	case strings.HasPrefix(path, basePath+"/") && r.Method == http.MethodDelete:
		apiKeyHash := strings.TrimPrefix(path, basePath+"/")
		if err := h.App.APIKeyAliasService.Delete(r.Context(), apiKeyHash); err != nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		response.JSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		response.MethodNotAllowed(w)
	}
}
