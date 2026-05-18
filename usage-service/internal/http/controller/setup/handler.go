package setup

import (
	"encoding/json"
	"net/http"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/app"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/http/response"
	setupsvc "github.com/seakee/cpa-manager-plus/usage-service/internal/service/setup"
)

type Handler struct {
	App *app.Context
}

func (h *Handler) Setup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req setupsvc.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.App.SetupService.Setup(r.Context(), req, r.Header.Get("Authorization"))
	if err != nil {
		response.Error(w, response.SetupErrorStatus(err), err)
		return
	}
	response.JSON(w, http.StatusOK, result)
}
