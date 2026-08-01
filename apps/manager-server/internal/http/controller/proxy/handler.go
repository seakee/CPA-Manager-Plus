package proxy

import (
	"errors"
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	proxysvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/proxy"
)

type Handler struct {
	App *app.Context
}

func (h *Handler) Management(w http.ResponseWriter, r *http.Request) {
	ok, err := h.App.AdminAuthService.VerifyHeader(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	if ok {
		if proxysvc.IsCPAPluginManagementPath(r.URL.Path) {
			h.App.ProxyService.ProxyPluginManagement(w, r, response.Error)
			return
		}
		h.App.ProxyService.ProxyManagement(w, r, response.Error)
		return
	}
	if !proxysvc.IsCPAPluginManagementPath(r.URL.Path) {
		response.Error(w, http.StatusUnauthorized, errors.New("invalid admin key"))
		return
	}
	h.App.ProxyService.ProxyPluginManagementWithCallerAuth(w, r, response.Error)
}

func (h *Handler) ModelList(w http.ResponseWriter, r *http.Request) {
	h.App.ProxyService.ProxyModelList(w, r, response.Error, response.MethodNotAllowed)
}

func (h *Handler) CPAResource(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet,
		http.MethodHead,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete:
	default:
		response.MethodNotAllowed(w)
		return
	}

	ok, err := h.App.AdminAuthService.VerifyHeader(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	// Only a verified CPAMP admin key authorizes substituting the saved CPA
	// management key. Otherwise preserve the caller's own authorization (or
	// lack of it) so public plugin resources remain reachable without privilege
	// elevation.
	if ok {
		h.App.ProxyService.ProxyPluginResource(w, r, response.Error)
		return
	}
	h.App.ProxyService.ProxyPluginResourceWithCallerAuth(w, r, response.Error)
}
