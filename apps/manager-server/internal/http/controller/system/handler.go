package system

import (
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
)

type Handler struct {
	App *app.Context
}

type dataMigrationStatus struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	LastEventID   int64  `json:"lastEventId"`
	TargetEventID int64  `json:"targetEventId"`
	ProcessedRows int64  `json:"processedRows"`
	ChangedRows   int64  `json:"changedRows"`
	StartedAtMS   int64  `json:"startedAtMs,omitempty"`
	UpdatedAtMS   int64  `json:"updatedAtMs"`
	FinishedAtMS  int64  `json:"finishedAtMs,omitempty"`
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
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	events, deadLetters, err := h.App.UsageService.Counts(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	status := h.App.CollectorService.Status()
	status.DeadLetters = deadLetters
	migration, err := h.App.Store.UsageCacheAccountingMigrationState(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{
		"service":     h.App.ServiceID,
		"dbPath":      h.App.Config.DBPath,
		"events":      events,
		"deadLetters": deadLetters,
		"collector":   status,
		"dataMigration": dataMigrationStatus{
			Name:          migration.Name,
			Status:        migration.Status,
			LastEventID:   migration.LastEventID,
			TargetEventID: migration.TargetEventID,
			ProcessedRows: migration.ProcessedRows,
			ChangedRows:   migration.ChangedRows,
			StartedAtMS:   migration.StartedAtMS,
			UpdatedAtMS:   migration.UpdatedAtMS,
			FinishedAtMS:  migration.FinishedAtMS,
		},
	})
}
