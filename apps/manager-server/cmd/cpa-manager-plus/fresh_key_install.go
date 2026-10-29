package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
)

// Canonical Admin authentication has already succeeded. No request may submit
// runtime identity, generation, exclusivity, credential, URL or proof flags.
func (a *freshApplication) initialClientKey(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation, id string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(405)
		return
	}
	if _, err := a.keys.Detail(r.Context(), id); err != nil {
		if errors.Is(err, freshkeys.ErrRequest) {
			w.WriteHeader(400)
		} else if errors.Is(err, freshkeys.ErrNotFound) {
			w.WriteHeader(404)
		} else {
			w.WriteHeader(503)
		}
		return
	}
	if r.Method == http.MethodPost {
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(415)
			return
		}
		var body *struct{}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128))
		d.DisallowUnknownFields()
		var trailing any
		if d.Decode(&body) != nil || body == nil || d.Decode(&trailing) != io.EOF {
			w.WriteHeader(400)
			return
		}
	}
	if a.keyApply == nil || !a.keyApply.Targets(op, freshkeyapply.SurfaceKeyInstall) {
		if r.Method == http.MethodPost {
			w.WriteHeader(409)
			return
		}
		writeFreshJSON(w, freshkeyapply.State{KeyID: id, ManagerStaged: true, RuntimeState: "effect_unknown"})
		return
	}
	grant := a.externalReadGrant(r.Context())
	var result freshkeyapply.State
	var err error
	if r.Method == http.MethodPost {
		result, err = a.keyApply.Apply(r.Context(), id, op.Strategy.TargetGeneration)
	} else {
		// The observation reads the CPA, so a credential it already refused is
		// answered locally instead of presented once more.
		if grant.Refused {
			writeExternalManagementRejected(w)
			return
		}
		result, err = a.keyApply.Observe(r.Context(), id, op.Strategy.TargetGeneration)
	}
	if err != nil {
		code := "s1_observation_unavailable"
		w.Header().Set("Content-Type", "application/json")
		switch {
		case errors.Is(err, freshkeys.ErrConflict):
			code = "s1_attempt_already_consumed"
			w.WriteHeader(409)
		case errors.Is(err, freshkeyapply.ErrGeneration):
			code = "s1_generation_mismatch"
			w.WriteHeader(409)
		case errors.Is(err, cpa.ErrSourceConflict):
			code = "s1_source_conflict"
			w.WriteHeader(409)
		case errors.Is(err, cpa.ErrManagementRejected):
			// The user's CPA refused the stored management credential: only
			// rebinding the connection resolves it, so it is never a retry. The
			// refusal is remembered, so the reads that follow send nothing.
			a.noteExternalRejection(r.Context(), grant, err)
			code = freshconfig.ReasonManagementRejected
			w.WriteHeader(409)
		case errors.Is(err, cpa.ErrExternalTargetMoved):
			// The connection no longer names the committed target: the target is
			// not missing, it moved, and only a rebind reaches it again.
			code = freshconfig.ReasonTargetMoved
			w.WriteHeader(409)
		case errors.Is(err, cpa.ErrExternalVersionUnsupported):
			// The user's CPA does not prove the v8 contract this Manager writes.
			// Reads keep working; no write is attempted until their CPA does.
			code = freshconfig.ReasonVersionUnsupported
			w.WriteHeader(409)
		case errors.Is(err, freshkeyapply.ErrUnavailable), errors.Is(err, freshkeyapply.ErrExternalUnavailable):
			code = "s1_host_proof_unavailable"
			w.WriteHeader(409)
		default:
			if errors.Is(err, cpa.ErrApplyUnknown) {
				code = "s1_write_unknown"
			}
			w.WriteHeader(503)
		}
		writeFreshJSON(w, struct {
			Code string `json:"code"`
		}{code})
		return
	}
	writeFreshJSON(w, result)
}
