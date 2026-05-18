package health

import (
	"net/http"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/http/response"
)

type Handler struct {
	ServiceID string
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"ok": true, "service": h.ServiceID})
}
