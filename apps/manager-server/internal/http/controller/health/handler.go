package health

import (
	"net"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
)

type Handler struct {
	App *app.Context
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"ok": true, "service": h.App.ServiceID})
}

func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if !isLoopbackRequest(r) {
		response.Error(w, http.StatusForbidden, http.ErrNotSupported)
		return
	}

	events, _, err := h.App.UsageService.Counts(r.Context())
	if err != nil {
		response.Error(w, http.StatusServiceUnavailable, err)
		return
	}
	status := h.App.CollectorService.Status()
	lastError := strings.TrimSpace(status.LastError)
	ready := status.Collector == "running" && lastError == ""
	httpStatus := http.StatusOK
	if !ready {
		httpStatus = http.StatusServiceUnavailable
	}

	response.JSON(w, httpStatus, map[string]any{
		"ok":               ready,
		"service":          h.App.ServiceID,
		"collector":        status.Collector,
		"events":           events,
		"lastConsumedAt":   status.LastConsumedAt,
		"lastInsertedAt":   status.LastInsertedAt,
		"totalInserted":    status.TotalInserted,
		"totalSkipped":     status.TotalSkipped,
		"lastErrorPresent": lastError != "",
	})
}

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.Trim(strings.TrimSpace(r.RemoteAddr), "[]")
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
