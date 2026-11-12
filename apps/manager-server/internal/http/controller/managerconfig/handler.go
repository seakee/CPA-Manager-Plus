package managerconfig

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type Handler struct {
	App *app.Context
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !h.authorizeRead(w, r) {
			return
		}
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
		ok, err := h.App.AdminAuthService.VerifySubmittedExternalConfigHeader(
			r.Context(),
			r.Header.Get("Authorization"),
			req.Config,
		)
		if err != nil {
			response.Error(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			response.Error(w, http.StatusUnauthorized, errors.New("invalid admin key"))
			return
		}
		result, err := h.App.ManagerConfigService.Update(r.Context(), req.Config)
		if err != nil {
			response.Error(w, response.ManagerConfigErrorStatus(err), err)
			return
		}
		response.JSON(w, http.StatusOK, result)
	case http.MethodPatch:
		// The request-monitoring switch is one field of the configuration, so it
		// has its own narrow write. A whole-document PUT makes every other field
		// the caller's responsibility, and a caller that only means to flip the
		// switch would then have to round-trip fields it does not own.
		ok, err := h.App.AdminAuthService.VerifyHeader(r.Context(), r.Header.Get("Authorization"))
		if err != nil {
			response.Error(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			response.Error(w, http.StatusUnauthorized, errors.New("invalid admin key"))
			return
		}
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil || req.Enabled == nil || decoder.Decode(new(any)) != io.EOF {
			response.Error(w, http.StatusBadRequest, errors.New("enabled is required"))
			return
		}
		result, err := h.App.ManagerConfigService.SetCollectorEnabled(r.Context(), *req.Enabled)
		if err != nil {
			response.Error(w, response.ManagerConfigErrorStatus(err), err)
			return
		}
		response.JSON(w, http.StatusOK, result)
	default:
		response.MethodNotAllowed(w)
	}
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

// ValidateCPAConnection is the strict server-side CPA connection validation
// endpoint used by the installer after a connection import. It requires CPAMP
// admin auth, ignores any client-supplied CPA Management Key, resolves the
// connection Manager Server actually uses, and propagates every CPA upstream
// failure (auth, network, 5xx) as a non-2xx response so the installer fails
// closed instead of committing a migration on a false positive.
func (h *Handler) ValidateCPAConnection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	ok, err := h.App.AdminAuthService.VerifyHeader(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		response.Error(w, http.StatusUnauthorized, errors.New("invalid admin key"))
		return
	}
	result, err := h.App.ManagerConfigService.ValidateCPAConnection(r.Context())
	if err != nil {
		response.Error(w, http.StatusBadGateway, err)
		return
	}
	if !result.Configured {
		response.Error(w, http.StatusConflict, errors.New("CPA connection is not configured"))
		return
	}
	response.JSON(w, http.StatusOK, result)
}
