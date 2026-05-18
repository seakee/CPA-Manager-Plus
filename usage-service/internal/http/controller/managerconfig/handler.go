package managerconfig

import (
	"encoding/json"
	"net/http"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/app"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/http/response"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/store"
)

type Handler struct {
	App *app.Context
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizeIfConfigured(w, r, h.App.ManagerConfigService) {
		return
	}

	switch r.Method {
	case http.MethodGet:
		result, err := h.App.ManagerConfigService.Get(r.Context())
		if err != nil {
			response.Error(w, http.StatusInternalServerError, err)
			return
		}
		response.JSON(w, http.StatusOK, result)
	case http.MethodPut:
		var req struct {
			Config store.ManagerConfig `json:"config"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		result, err := h.App.ManagerConfigService.Update(r.Context(), req.Config)
		if err != nil {
			response.Error(w, response.ManagerConfigErrorStatus(err), err)
			return
		}
		response.JSON(w, http.StatusOK, result)
	default:
		response.MethodNotAllowed(w)
	}
}
