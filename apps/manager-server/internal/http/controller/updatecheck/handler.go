package updatecheck

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
)

type Handler struct{ App *app.Context }

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	svc := h.App.UpdateCheckService
	if svc == nil {
		http.Error(w, "update check unavailable", http.StatusServiceUnavailable)
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, "/usage-service/updates")
	var payload any
	var err error
	switch {
	case suffix == "" && r.Method == http.MethodGet:
		payload, err = svc.Status(r.Context())
	case suffix == "/check" && r.Method == http.MethodPost:
		payload, err = svc.Check(r.Context())
	case suffix == "/notification" && r.Method == http.MethodPost:
		payload, err = svc.Claim(r.Context())
	case suffix == "/channel" && r.Method == http.MethodPut:
		var input struct {
			Preference string `json:"channel_preference"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		if input.Preference != "auto" && input.Preference != "stable" && input.Preference != "rc" && input.Preference != "beta" {
			http.Error(w, "invalid channel", 400)
			return
		}
		payload, err = svc.SetChannel(r.Context(), input.Preference)
	case suffix == "/dismiss" && r.Method == http.MethodPost:
		var input struct {
			Version string `json:"version"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		err = svc.Dismiss(r.Context(), input.Version)
		payload = map[string]bool{"ok": err == nil}
	default:
		response.MethodNotAllowed(w)
		return
	}
	if err != nil {
		http.Error(w, "update state unavailable", http.StatusServiceUnavailable)
		return
	}
	response.JSON(w, http.StatusOK, payload)
}
