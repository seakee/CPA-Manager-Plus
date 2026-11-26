package main

import (
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
)

type trialHoldServerHandler struct {
	startup *startupHTTPHandler
	gate    *middleware.TrialHoldGate
	wrapped http.Handler
}

func newTrialHoldServerHandler(startup *startupHTTPHandler) (*trialHoldServerHandler, func()) {
	gate := middleware.NewTrialHoldGateFromEnv()
	middleware.SetProcessTrialHoldGate(gate)
	cleanup := func() {
		middleware.SetProcessTrialHoldGate(nil)
	}
	return &trialHoldServerHandler{
		startup: startup,
		gate:    gate,
		wrapped: gate.Wrap(startup),
	}, cleanup
}

func (h *trialHoldServerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.wrapped == nil {
		http.NotFound(w, r)
		return
	}
	h.wrapped.ServeHTTP(w, r)
}

func (h *trialHoldServerHandler) BeginShutdown() {
	if h != nil && h.startup != nil {
		h.startup.BeginShutdown()
	}
}
