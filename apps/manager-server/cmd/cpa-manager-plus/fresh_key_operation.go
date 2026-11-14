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
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		w.WriteHeader(http.StatusBadRequest)
		return false
	}
	return true
}

func keyOperationCode(err error) (int, string) {
	status, code := http.StatusServiceUnavailable, "key_operation_unknown"
	switch {
	case errors.Is(err, freshkeys.ErrRequest):
		status, code = http.StatusBadRequest, "key_operation_invalid"
	case errors.Is(err, freshkeys.ErrNotFound):
		status, code = http.StatusNotFound, "key_operation_not_found"
	case errors.Is(err, freshkeyapply.ErrLastKey):
		status, code = http.StatusConflict, "last_effective_key"
	case errors.Is(err, freshkeys.ErrConflict):
		status, code = http.StatusConflict, "key_operation_conflict"
	case errors.Is(err, freshkeyapply.ErrGeneration):
		status, code = http.StatusConflict, "key_operation_generation_mismatch"
	case errors.Is(err, cpa.ErrManagementRejected):
		status, code = http.StatusConflict, freshconfig.ReasonManagementRejected
	case errors.Is(err, cpa.ErrExternalTargetMoved):
		status, code = http.StatusConflict, freshconfig.ReasonTargetMoved
	case errors.Is(err, cpa.ErrExternalVersionUnsupported):
		status, code = http.StatusConflict, freshconfig.ReasonVersionUnsupported
	case errors.Is(err, freshkeyapply.ErrUnavailable), errors.Is(err, freshkeyapply.ErrExternalUnavailable):
		status, code = http.StatusConflict, "key_operation_host_proof_required"
	case errors.Is(err, cpa.ErrSourceConflict):
		status, code = http.StatusConflict, "key_operation_source_conflict"
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
	detail := ""
	if errors.Is(err, cpa.ErrManagementClientForbidden) {
		detail = "client_forbidden"
	}
	for _, name := range proxiedEntityHeaders {
		w.Header().Del(name)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeFreshJSON(w, struct {
		Code   string                        `json:"code"`
		Detail string                        `json:"detail,omitempty"`
		Result freshkeyapply.OperationResult `json:"result"`
	}{code, detail, result})
}

func (a *freshApplication) prepareClientKeyOperation(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation, target string) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !a.keyOperationTarget(op) {
		w.WriteHeader(http.StatusConflict)
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
	authority := a.keyApply.ForOperation(op)
	result, err := authority.Prepare(a.withExternalSavedCredentialGate(r.Context()), body.RequestID, target, body.KeeperID, body.Mode, op.Strategy.TargetGeneration)
	if result.Operation != nil && result.Operation.Outcome == "committed" && result.Operation.Mode != "add" && a.usageProjection != nil {
		a.usageProjection.Wake()
	}
	keyOperationReply(w, result, err)
}

func (a *freshApplication) preflightClientKeyOperation(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation, target, keeper, mode string) {
	if !a.keyOperationTarget(op) {
		w.WriteHeader(http.StatusConflict)
		return
	}
	if a.writeExternalSavedCredentialPrecheck(w, r.Context(), func() {
		keyOperationPreflightReply(w, false, freshconfig.ReasonManagementRejected)
	}) {
		return
	}
	authority := a.keyApply.ForOperation(op)
	err := authority.Preflight(a.withExternalSavedCredentialGate(r.Context()), target, keeper, mode, op.Strategy.TargetGeneration)
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

func keyOperationPreflightReply(w http.ResponseWriter, ready bool, reason string) {
	writeFreshJSON(w, struct {
		Ready  bool   `json:"ready"`
		Reason string `json:"reason"`
	}{ready, reason})
}

func (a *freshApplication) clientKeyOperation(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation, resource string) {
	if !a.keyOperationTarget(op) {
		w.WriteHeader(http.StatusConflict)
		return
	}
	authority := a.keyApply.ForOperation(op)
	id := strings.TrimPrefix(resource, "api-keys/operations/")
	if r.Method == http.MethodGet && !strings.Contains(id, "/") {
		if a.writeExternalSavedCredentialPrecheck(w, r.Context(), func() {
			writeExternalManagementRejected(w)
		}) {
			return
		}
		result, err := authority.CheckOperation(a.withExternalSavedCredentialGate(r.Context()), id, op.Strategy.TargetGeneration)
		if result.Operation != nil && result.Operation.Outcome == "committed" && result.Operation.Mode != "add" && a.usageProjection != nil {
			a.usageProjection.Wake()
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
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if a.writeExternalSavedCredentialPrecheck(w, r.Context(), func() {
			writeExternalManagementRejected(w)
		}) {
			return
		}
		result, err := authority.ContinueOperation(a.withExternalSavedCredentialGate(r.Context()), strings.TrimSuffix(id, "/continue"), op.Strategy.TargetGeneration)
		if result.Operation != nil && result.Operation.Outcome == "committed" && result.Operation.Mode != "add" && a.usageProjection != nil {
			a.usageProjection.Wake()
		}
		keyOperationReply(w, result, err)
		return
	}
	w.WriteHeader(http.StatusMethodNotAllowed)
}
