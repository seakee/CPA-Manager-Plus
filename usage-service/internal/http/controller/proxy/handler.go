package proxy

import (
	"net/http"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/app"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/http/response"
)

type Handler struct {
	App *app.Context
}

func (h *Handler) Management(w http.ResponseWriter, r *http.Request) {
	h.App.ProxyService.ProxyManagement(w, r, response.Error)
}

func (h *Handler) ModelList(w http.ResponseWriter, r *http.Request) {
	h.App.ProxyService.ProxyModelList(w, r, response.Error, response.MethodNotAllowed)
}
