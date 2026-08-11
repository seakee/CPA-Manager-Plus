package accountaction

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	accountactionsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/accountaction"
)

type Handler struct {
	App *app.Context
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}

	path := strings.TrimRight(r.URL.Path, "/")
	if path == "/v0/management/account-action-candidates" {
		h.handleList(w, r)
		return
	}

	if path == "/v0/management/account-action-candidates/resolve-reauth-by-auth-file" {
		h.handleResolveReauthByAuthFile(w, r)
		return
	}

	if !strings.HasPrefix(path, "/v0/management/account-action-candidates/") {
		response.MethodNotAllowed(w)
		return
	}
	idRaw := strings.TrimPrefix(path, "/v0/management/account-action-candidates/")
	parts := strings.Split(idRaw, "/")
	if len(parts) != 2 {
		response.MethodNotAllowed(w)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		response.Error(w, http.StatusBadRequest, errors.New("candidate id is required"))
		return
	}

	switch parts[1] {
	case "ignore":
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		item, err := h.App.AccountActionService.Ignore(r.Context(), id)
		h.writeCandidateResult(w, item, err)
	case "resolve":
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		item, err := h.App.AccountActionService.Resolve(r.Context(), id)
		h.writeCandidateResult(w, item, err)
	case "enable":
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		item, err := h.App.AccountActionService.Enable(r.Context(), id)
		h.writeCandidateResult(w, item, err)
	case "auth-file":
		if r.Method != http.MethodDelete {
			response.MethodNotAllowed(w)
			return
		}
		item, err := h.App.AccountActionService.DeleteAuthFile(r.Context(), id)
		h.writeCandidateResult(w, item, err)
	default:
		response.MethodNotAllowed(w)
	}
}

func (h *Handler) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	result, err := h.App.AccountActionService.List(r.Context(), r.URL.Query().Get("status"), limit)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	response.JSON(w, http.StatusOK, result)
}

func (h *Handler) handleResolveReauthByAuthFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var body struct {
		FileName string `json:"fileName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		response.Error(w, http.StatusBadRequest, errors.New("invalid body"))
		return
	}
	if strings.TrimSpace(body.FileName) == "" {
		response.Error(w, http.StatusBadRequest, errors.New("auth file name is required"))
		return
	}
	resolved, err := h.App.AccountActionService.ResolveReauthByAuthFileName(r.Context(), body.FileName)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"resolved": resolved})
}

func (h *Handler) writeCandidateResult(w http.ResponseWriter, item any, err error) {
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, accountactionsvc.ErrCandidateNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, accountactionsvc.ErrCandidateConflict) || errors.Is(err, accountactionsvc.ErrCandidateNotPending) {
			status = http.StatusConflict
		} else if strings.Contains(err.Error(), "usage service is not configured") {
			status = http.StatusPreconditionRequired
		}
		response.Error(w, status, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"item": item})
}
