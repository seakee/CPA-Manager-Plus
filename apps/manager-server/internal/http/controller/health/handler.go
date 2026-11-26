package health

import (
	"net/http"
	"os"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
)

type Handler struct {
	ServiceID       string
	TrialHoldActive func() bool
}

type healthResponse struct {
	OK               bool   `json:"ok"`
	Service          string `json:"service"`
	RunningVersion   string `json:"running_version,omitempty"`
	BaseImageVersion string `json:"base_image_version,omitempty"`
	ActiveSlot       string `json:"active_slot,omitempty"`
	TrialHold        bool   `json:"trial_hold,omitempty"`
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	trialHold := h.isTrialHoldActive()
	baseEnv := strings.TrimSpace(os.Getenv("CPAMP_BASE_VERSION"))
	activeSlot := buildinfo.ActiveSlot()

	payload := healthResponse{
		OK:      true,
		Service: h.ServiceID,
	}
	if baseEnv != "" || activeSlot != "" || trialHold {
		payload.RunningVersion = buildinfo.EffectiveVersion()
		payload.BaseImageVersion = buildinfo.BaseImageVersion()
		payload.ActiveSlot = activeSlot
		payload.TrialHold = trialHold
	}
	response.JSON(w, http.StatusOK, payload)
}

func (h *Handler) isTrialHoldActive() bool {
	if h != nil && h.TrialHoldActive != nil {
		return h.TrialHoldActive()
	}
	return middleware.IsProcessTrialHoldActive()
}
