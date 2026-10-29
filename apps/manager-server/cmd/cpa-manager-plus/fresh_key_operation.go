package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
)

func (a *freshApplication) keyOperationTarget(op bootstrap.FreshOperation) bool {
	return a.keyApply.KeyOperationsEnabled() && a.keyApply.Targets(op, freshkeyapply.SurfaceKeyOperation)
}
func decodeKeyOperation(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		w.WriteHeader(415)
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		w.WriteHeader(400)
		return false
	}
	return true
}
func keyOperationReply(w http.ResponseWriter, result freshkeyapply.OperationResult, err error) {
	if err == nil {
		writeFreshJSON(w, result)
		return
	}
	status, code := 503, "key_operation_unknown"
	switch {
	case errors.Is(err, freshkeys.ErrRequest):
		status, code = 400, "key_operation_invalid"
	case errors.Is(err, freshkeys.ErrNotFound):
		status, code = 404, "key_operation_not_found"
	case errors.Is(err, freshkeyapply.ErrLastKey):
		status, code = 409, "last_effective_key"
	case errors.Is(err, freshkeys.ErrConflict):
		status, code = 409, "key_operation_conflict"
	case errors.Is(err, freshkeyapply.ErrGeneration):
		status, code = 409, "key_operation_generation_mismatch"
	case errors.Is(err, cpa.ErrManagementRejected):
		// The user's CPA refused the stored management credential. Only rebinding
		// the connection resolves it, so the page must not offer a retry.
		status, code = 409, freshconfig.ReasonManagementRejected
	case errors.Is(err, cpa.ErrExternalTargetMoved):
		// The connection no longer names the committed target. The journal is
		// intact; the target moved, and only a rebind reaches it again.
		status, code = 409, freshconfig.ReasonTargetMoved
	case errors.Is(err, cpa.ErrExternalVersionUnsupported):
		// The user's CPA does not prove the v8 contract this Manager writes, so
		// no step was sent. Reads keep working; the write waits for a CPA that
		// serves the surface it would target.
		status, code = 409, freshconfig.ReasonVersionUnsupported
	case errors.Is(err, freshkeyapply.ErrUnavailable), errors.Is(err, freshkeyapply.ErrExternalUnavailable):
		status, code = 409, "key_operation_host_proof_required"
	case errors.Is(err, cpa.ErrSourceConflict):
		status, code = 409, "key_operation_source_conflict"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeFreshJSON(w, struct {
		Code   string                        `json:"code"`
		Result freshkeyapply.OperationResult `json:"result"`
	}{code, result})
}
func (a *freshApplication) prepareClientKeyOperation(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation, target string) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if !a.keyOperationTarget(op) {
		w.WriteHeader(409)
		return
	}
	var body struct {
		RequestID string `json:"requestId"`
		KeeperID  string `json:"keeperId"`
		Mode      string `json:"mode"`
	}
	if !decodeKeyOperation(w, r, &body) {
		return
	}
	result, err := a.keyApply.Prepare(r.Context(), body.RequestID, target, body.KeeperID, body.Mode, op.Strategy.TargetGeneration)
	keyOperationReply(w, result, err)
}
func (a *freshApplication) clientKeyOperation(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation, resource string) {
	if !a.keyOperationTarget(op) {
		w.WriteHeader(409)
		return
	}
	id := strings.TrimPrefix(resource, "api-keys/operations/")
	if r.Method == http.MethodGet && !strings.Contains(id, "/") {
		// A credential the user's CPA already refused is not presented again: this
		// page polls a journaled step, and five failed authentications ban the
		// address for thirty minutes.
		grant := a.externalReadGrant(r.Context())
		if grant.Refused {
			writeExternalManagementRejected(w)
			return
		}
		result, err := a.keyApply.CheckOperation(r.Context(), id, op.Strategy.TargetGeneration)
		if errors.Is(err, cpa.ErrManagementRejected) {
			// The poll is where the refusal is first known, so it is recorded and
			// the reads that follow answer it without contacting the CPA.
			a.noteExternalRejection(r.Context(), grant, err)
		}
		keyOperationReply(w, result, err)
		return
	}
	if r.Method == http.MethodPost && strings.HasSuffix(id, "/continue") && strings.Count(id, "/") == 1 {
		var body map[string]any
		if !decodeKeyOperation(w, r, &body) {
			return
		}
		if body == nil || len(body) != 0 {
			w.WriteHeader(400)
			return
		}
		result, err := a.keyApply.ContinueOperation(r.Context(), strings.TrimSuffix(id, "/continue"), op.Strategy.TargetGeneration)
		keyOperationReply(w, result, err)
		return
	}
	w.WriteHeader(405)
}
