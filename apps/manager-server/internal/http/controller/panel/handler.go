package panel

import (
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
)

type Handler struct {
	App *app.Context
}

func (h *Handler) ManagementHTML(w http.ResponseWriter, r *http.Request) {
	h.App.PanelService.ServeManagementHTML(w, r, response.Error)
}
