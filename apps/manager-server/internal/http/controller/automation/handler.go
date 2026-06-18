package automation

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	automationsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/automation"
)

type Handler struct {
	App     *app.Context
	service *automationsvc.Service
}

func New(appCtx *app.Context) *Handler {
	service := appCtx.AccountProcessingPolicyService
	if service == nil {
		service = automationsvc.New(appCtx.Config, appCtx.Store)
	}
	return &Handler{App: appCtx, service: service}
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !h.authorizeRead(w, r) {
			return
		}
		status, err := h.service.Status(r.Context())
		if err != nil {
			response.Error(w, http.StatusInternalServerError, err)
			return
		}
		response.JSON(w, http.StatusOK, status)
	case http.MethodPatch:
		if !h.authorizeWrite(w, r) {
			return
		}
		var req automationsvc.UpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		result, err := h.service.Update(r.Context(), req)
		if err != nil {
			response.Error(w, response.ManagerConfigErrorStatus(err), err)
			return
		}
		if h.App.AutomationRuntimeService != nil {
			if err := h.App.AutomationRuntimeService.Reload(r.Context()); err != nil {
				response.Error(w, http.StatusInternalServerError, err)
				return
			}
		}
		response.JSON(w, http.StatusOK, result)
	default:
		response.MethodNotAllowed(w)
	}
}

func (h *Handler) authorizeWrite(w http.ResponseWriter, r *http.Request) bool {
	ok, err := h.App.AdminAuthService.VerifyHeader(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return false
	}
	if ok {
		return true
	}
	response.Error(w, http.StatusUnauthorized, errors.New("invalid admin key"))
	return false
}

func (h *Handler) authorizeRead(w http.ResponseWriter, r *http.Request) bool {
	ok, err := h.App.AdminAuthService.VerifyPanelHeader(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return false
	}
	if ok {
		return true
	}
	setup, setupOK, err := h.App.ManagerConfigService.ResolveSetup(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return false
	}
	if !setupOK || setup.ManagementKey == "" {
		return true
	}
	response.Error(w, http.StatusUnauthorized, errors.New("invalid admin key"))
	return false
}
