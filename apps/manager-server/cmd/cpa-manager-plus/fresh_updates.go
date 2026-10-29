package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

// Called only behind canonical routing and the Fresh Admin verifier.
//
// The Fresh version-update channel stays New Managed only, and deliberately does
// not follow `freshkeyapply.ManagerOwnedStrategy`. It is not an installation
// authority: the update check compares the Manager's own build against upstream,
// and its Fresh wiring is scheduled for Later. Admitting an adopted or an
// External checkpoint here would publish a channel decision to an installation
// whose update story this lane does not define.
func (a *freshApplication) updateChannel(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation) {
	if !op.GatewayPending || op.Strategy.Strategy != "" && op.Strategy.Strategy != bootstrap.NewManaged {
		w.WriteHeader(http.StatusConflict)
		return
	}
	var preference string
	var err error
	switch r.Method {
	case http.MethodGet:
		preference, err = a.updates.SavedChannel(r.Context())
	case http.MethodPut:
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		var input struct {
			Preference string `json:"channel_preference"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var trailing any
		if d.Decode(&trailing) != io.EOF || (input.Preference != "auto" && input.Preference != "stable" && input.Preference != "rc" && input.Preference != "beta") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, err = a.updates.PersistChannel(r.Context(), input.Preference)
		preference = input.Preference
	default:
		w.Header().Set("Allow", "GET, PUT")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	writeFreshJSON(w, struct {
		Preference string `json:"channel_preference"`
	}{preference})
}

func (a *freshApplication) runFreshUpdateChecks(ctx context.Context) {
	// The private Console can precede CPA staging. Manager software discovery
	// consumes the setting already; it never requires or changes CPA state.
	// Retire this worker if the installation leaves the admitted New slice.
	op, err := a.current(ctx)
	if err != nil || !op.GatewayPending || op.Strategy.Strategy != "" && op.Strategy.Strategy != bootstrap.NewManaged {
		return
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.updates.Run(workerCtx)
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			op, err := a.current(ctx)
			if err != nil || !op.GatewayPending || op.Strategy.Strategy != "" && op.Strategy.Strategy != bootstrap.NewManaged {
				return
			}
		}
	}
}
