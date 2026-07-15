package codexinspection

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	codexsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/codexinspection"
)

type Handler struct {
	App *app.Context
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}

	path := strings.Trim(strings.TrimRight(r.URL.Path, "/"), " ")
	switch {
	case path == "/v0/management/codex-inspection/run":
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		result, err := h.App.CodexInspectionService.Run(context.WithoutCancel(r.Context()), codexsvc.RunRequest{
			TriggerType: "manual",
			TriggerKey:  "manual",
		})
		if err != nil {
			response.Error(w, codexInspectionErrorStatus(err), err)
			return
		}
		response.JSON(w, http.StatusOK, result)
	case path == "/v0/management/codex-inspection/runs":
		if r.Method != http.MethodGet {
			response.MethodNotAllowed(w)
			return
		}
		limit := 20
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		runs, err := h.App.CodexInspectionService.ListRuns(r.Context(), limit)
		if err != nil {
			response.Error(w, http.StatusInternalServerError, err)
			return
		}
		response.JSON(w, http.StatusOK, map[string]any{"items": runs})
	default:
		if !strings.HasPrefix(path, "/v0/management/codex-inspection/runs/") {
			response.MethodNotAllowed(w)
			return
		}
		idRaw := strings.TrimPrefix(path, "/v0/management/codex-inspection/runs/")
		actionPath := false
		if strings.HasSuffix(idRaw, "/actions") {
			actionPath = true
			idRaw = strings.TrimSuffix(idRaw, "/actions")
		}
		id, err := strconv.ParseInt(idRaw, 10, 64)
		if err != nil || id <= 0 {
			if err == nil {
				err = errors.New("run id is required")
			}
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		if actionPath {
			if r.Method != http.MethodPost {
				response.MethodNotAllowed(w)
				return
			}
			var req codexsvc.ExecuteActionsRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				response.Error(w, http.StatusBadRequest, err)
				return
			}
			result, err := h.App.CodexInspectionService.ExecuteManualActions(r.Context(), id, req)
			if err != nil {
				response.Error(w, codexInspectionErrorStatus(err), err)
				return
			}
			response.JSON(w, http.StatusOK, result)
			return
		}
		if r.Method != http.MethodGet {
			response.MethodNotAllowed(w)
			return
		}
		detail, err := h.App.CodexInspectionService.GetRun(r.Context(), id)
		if err != nil {
			response.Error(w, codexInspectionErrorStatus(err), err)
			return
		}
		response.JSON(w, http.StatusOK, detail)
	}
}

func codexInspectionErrorStatus(err error) int {
	switch {
	case errors.Is(err, codexsvc.ErrRunNotFound):
		return http.StatusNotFound
	case errors.Is(err, codexsvc.ErrRunAlreadyActive),
		errors.Is(err, codexsvc.ErrRunNotCompleted):
		return http.StatusConflict
	case errors.Is(err, codexsvc.ErrNotConfigured):
		return http.StatusPreconditionFailed
	case errors.Is(err, codexsvc.ErrActionIDsRequired),
		errors.Is(err, codexsvc.ErrNoActionableResults),
		errors.Is(err, codexsvc.ErrInvalidActionOverride):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
