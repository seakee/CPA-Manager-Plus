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
	authority := a.keyApply.ForOperation(op)
	return authority != nil && authority.KeyOperationsEnabled() && authority.Targets(op, freshkeyapply.SurfaceKeyOperation)
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

// keyOperationCode is the one vocabulary a refused key operation is reported in.
// The preflight answers with the same code the prepare it predicts would return,
// so the page states the limit instead of translating a second set of reasons.
func keyOperationCode(err error) (int, string) {
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
	return status, code
}

func keyOperationReply(w http.ResponseWriter, result freshkeyapply.OperationResult, err error) {
	if err == nil {
		writeFreshJSON(w, result)
		return
	}
	if errors.Is(err, errExternalCredentialWaitCancelled) {
		writeExternalCredentialWaitCancelled(w)
		return
	}
	if errors.Is(err, cpa.ErrExternalRejectionUnavailable) {
		writeExternalAuthorityUnavailable(w)
		return
	}
	status, code := keyOperationCode(err)
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
	if a.writeExternalSavedCredentialPrecheck(w, r.Context(), func() {
		writeExternalManagementRejected(w)
	}) {
		return
	}
	result, err := a.keyApply.Prepare(a.withExternalSavedCredentialGate(r.Context()), body.RequestID, target, body.KeeperID, body.Mode, op.Strategy.TargetGeneration)
	keyOperationReply(w, result, err)
}

// preflightClientKeyOperation answers whether one revoke or replace would be
// accepted, and the reason when it would not. It writes nothing: the drawer asks
// it before it offers the submit, so every remaining limit — a destination key the
// Manager never staged, the installation's own key, the last effective key — is
// named where the operator is choosing instead of surfacing as a conflict.
func (a *freshApplication) preflightClientKeyOperation(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation, target, keeper, mode string) {
	if !a.keyOperationTarget(op) {
		w.WriteHeader(409)
		return
	}
	// The preflight is asked again on every selection the drawer shows, so it is a
	// poll. A credential the user's CPA already refused is never presented by it:
	// five failed authentications ban the address for thirty minutes, and the
	// answer is the same blocker the submit would give, written locally.
	if a.writeExternalSavedCredentialPrecheck(w, r.Context(), func() {
		keyOperationPreflightReply(w, false, freshconfig.ReasonManagementRejected)
	}) {
		return
	}
	err := a.keyApply.Preflight(a.withExternalSavedCredentialGate(r.Context()), target, keeper, mode, op.Strategy.TargetGeneration)
	if errors.Is(err, errExternalCredentialWaitCancelled) {
		writeExternalCredentialWaitCancelled(w)
		return
	}
	if errors.Is(err, cpa.ErrExternalRejectionUnavailable) {
		writeExternalAuthorityUnavailable(w)
		return
	}
	reason := ""
	if err != nil {
		_, reason = keyOperationCode(err)
	}
	keyOperationPreflightReply(w, err == nil, reason)
}

// keyOperationPreflightReply writes the readiness verdict the drawer renders. Every
// limit keeps this one shape, including the typed External blockers: the page
// names each of them in place and disables the submit, so a refusal is never a
// second error channel the drawer would have to translate.
func keyOperationPreflightReply(w http.ResponseWriter, ready bool, reason string) {
	writeFreshJSON(w, struct {
		Ready  bool   `json:"ready"`
		Reason string `json:"reason"`
	}{ready, reason})
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
		if a.writeExternalSavedCredentialPrecheck(w, r.Context(), func() {
			writeExternalManagementRejected(w)
		}) {
			return
		}
		result, err := a.keyApply.CheckOperation(a.withExternalSavedCredentialGate(r.Context()), id, op.Strategy.TargetGeneration)
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
		if a.writeExternalSavedCredentialPrecheck(w, r.Context(), func() {
			writeExternalManagementRejected(w)
		}) {
			return
		}
		result, err := a.keyApply.ContinueOperation(a.withExternalSavedCredentialGate(r.Context()), strings.TrimSuffix(id, "/continue"), op.Strategy.TargetGeneration)
		keyOperationReply(w, result, err)
		return
	}
	w.WriteHeader(405)
}
