package proxy

import (
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
)

type Handler struct {
	App *app.Context
}

func (h *Handler) Management(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizeAdmin(w, r, h.App.AdminAuthService) {
		return
	}
	h.App.ProxyService.ProxyManagement(w, r, response.Error)
}

func (h *Handler) CPA(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizeAdmin(w, r, h.App.AdminAuthService) {
		return
	}
	h.App.ProxyService.ProxyCPA(w, r, response.Error)
}

func (h *Handler) CPAResource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		response.MethodNotAllowed(w)
		return
	}
	h.App.ProxyService.ProxyCPA(w, r, response.Error)
}

func (h *Handler) ModelList(w http.ResponseWriter, r *http.Request) {
	h.App.ProxyService.ProxyModelList(w, r, response.Error, response.MethodNotAllowed)
}
