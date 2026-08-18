package update

import (
	"errors"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
)

type Handler struct {
	App *app.Context
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/v0/management/update/capability" && r.Method == http.MethodGet:
		response.JSON(w, http.StatusOK, h.App.UpdateService.Capability())
	case path == "/v0/management/update/check" && r.Method == http.MethodGet:
		result, err := h.App.UpdateService.Check(r.Context())
		if err != nil {
			response.Error(w, http.StatusBadGateway, err)
			return
		}
		response.JSON(w, http.StatusOK, result)
	case path == "/v0/management/update/status" && r.Method == http.MethodGet:
		result, found, err := h.App.UpdateService.Status()
		if err != nil {
			response.Error(w, http.StatusInternalServerError, err)
			return
		}
		response.JSON(w, http.StatusOK, map[string]any{"found": found, "status": result})
	case path == "/v0/management/update/plan" && r.Method == http.MethodPost:
		result, err := h.App.UpdateService.Plan(r.Context(), h.App.Config.HTTPAddr)
		if err != nil {
			response.Error(w, http.StatusConflict, err)
			return
		}
		response.JSON(w, http.StatusCreated, result)
	case path == "/v0/management/update/apply" && r.Method == http.MethodPost:
		if h.App.ShutdownRequester == nil {
			response.Error(w, http.StatusConflict, errors.New("managed shutdown is unavailable"))
			return
		}
		result, err := h.App.UpdateService.Apply(h.App.ShutdownRequester.RequestShutdown)
		if err != nil {
			response.Error(w, http.StatusConflict, err)
			return
		}
		response.JSON(w, http.StatusAccepted, result)
	default:
		response.MethodNotAllowed(w)
	}
}
