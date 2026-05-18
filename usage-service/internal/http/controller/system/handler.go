package system

import (
	"net/http"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/app"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/http/response"
)

type Handler struct {
	App *app.Context
}

func (h *Handler) Info(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	info, err := h.App.SetupService.Info(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	response.JSON(w, http.StatusOK, info)
}

func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if !middleware.AuthorizeIfConfigured(w, r, h.App.ManagerConfigService) {
		return
	}
	events, deadLetters, err := h.App.UsageService.Counts(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	status := h.App.CollectorService.Status()
	status.DeadLetters = deadLetters
	response.JSON(w, http.StatusOK, map[string]any{
		"service":     h.App.ServiceID,
		"dbPath":      h.App.Config.DBPath,
		"events":      events,
		"deadLetters": deadLetters,
		"collector":   status,
	})
}
