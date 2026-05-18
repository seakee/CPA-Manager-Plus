package panel

import (
	"net/http"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/app"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/http/response"
)

type Handler struct {
	App *app.Context
}

func (h *Handler) ManagementHTML(w http.ResponseWriter, r *http.Request) {
	h.App.PanelService.ServeManagementHTML(w, response.Error)
}
