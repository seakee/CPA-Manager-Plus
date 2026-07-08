package monitoring

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	monitoringsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/monitoring"
)

type Handler struct {
	App *app.Context
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}

	path := strings.TrimRight(r.URL.Path, "/")
	if path == "/v0/management/monitoring/header-snapshots" {
		h.handleHeaderSnapshots(w, r)
		return
	}
	if path == "/v0/management/monitoring/account-history" {
		h.handleAccountHistory(w, r)
		return
	}
	if path != "/v0/management/monitoring/analytics" {
		response.MethodNotAllowed(w)
		return
	}
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	var req monitoringsvc.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	if err := validateRequest(req); err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}

	result, err := h.App.MonitoringService.Analytics(r.Context(), req)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	response.JSON(w, http.StatusOK, result)
}

func (h *Handler) handleAccountHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req monitoringsvc.AccountHistoryRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	if err := validateAccountHistoryRequest(req); err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.App.MonitoringService.AccountHistory(r.Context(), req)
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	response.JSON(w, http.StatusOK, result)
}

func (h *Handler) handleHeaderSnapshots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	query := r.URL.Query()
	days, err := parseOptionalInt(query.Get("days"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	limit, err := parseOptionalInt(query.Get("limit"))
	if err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.App.MonitoringService.HeaderSnapshots(r.Context(), monitoringsvc.HeaderSnapshotsRequest{
		Days:  days,
		Limit: limit,
	})
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	response.JSON(w, http.StatusOK, result)
}

func validateRequest(req monitoringsvc.Request) error {
	if req.FromMS <= 0 || req.ToMS <= 0 || req.FromMS >= req.ToMS {
		return errors.New("from_ms and to_ms are required and from_ms must be less than to_ms")
	}
	if req.Include.EventsPage != nil && req.Include.EventsPage.Limit > 50000 {
		return errors.New("events_page.limit must be less than or equal to 50000")
	}
	return nil
}

func validateAccountHistoryRequest(req monitoringsvc.AccountHistoryRequest) error {
	if len(req.Accounts) == 0 {
		return errors.New("accounts are required")
	}
	if len(req.Accounts) > 200 {
		return errors.New("accounts must be less than or equal to 200")
	}
	return nil
}

func parseOptionalInt(value string) (int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, err
	}
	if parsed < 0 {
		return 0, errors.New("query value must be greater than or equal to 0")
	}
	return parsed, nil
}
