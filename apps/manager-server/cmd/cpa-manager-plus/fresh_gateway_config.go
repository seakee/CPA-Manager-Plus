package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
)

// maxGatewayConfigBody bounds one page write. A whole payload rule family is the
// largest legal body; YAML, raw configuration and recovery bytes are never
// accepted here.
const maxGatewayConfigBody = 256 << 10

// exactObject rejects duplicate/case-folded/unknown keys, null, trailing data and
// wrong container types. It is the strict shape of every gateway write body.
func exactObject(b []byte, keys ...string) bool {
	d := json.NewDecoder(bytes.NewReader(b))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		t, e = d.Token()
		k, ok := t.(string)
		if e != nil || !ok || seen[k] {
			return false
		}
		allowed := false
		for _, name := range keys {
			if name == k {
				allowed = true
			}
		}
		if !allowed {
			return false
		}
		seen[k] = true
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return false
		}
	}
	t, e = d.Token()
	return e == nil && t == json.Delim('}') && len(seen) == len(keys) && d.Decode(new(any)) == io.EOF
}

func gatewayConfigBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Content-Encoding") != "" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return nil, false
	}
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, maxGatewayConfigBody))
	if e != nil {
		w.WriteHeader(http.StatusBadRequest)
		return nil, false
	}
	return b, true
}

// gatewayConfigReply writes the page contract: a journaled operation with its
// separate effectiveness. HTTP success is persisted evidence only; CPA reloads
// asynchronously, so effectiveness is never claimed here.
func gatewayConfigReply(w http.ResponseWriter, v any, e error) {
	if o, ok := v.(freshstore.ConfigGroupOperation); ok {
		v = struct {
			Operation freshstore.ConfigGroupOperation `json:"operation"`
			Effective string                          `json:"effective"`
		}{o, "unknown"}
	}
	if e == nil {
		writeFreshJSON(w, v)
		return
	}
	var rejection freshconfig.Rejection
	var refusal freshconfig.CPARejection
	var writeError cpa.ConfigWriteError
	code, status := "configuration_unavailable", http.StatusServiceUnavailable
	// A CPA refusal carries a field and a redacted diagnostic; both branches
	// answer the same bounded cpa_rejected_config shape.
	replyRefusal := func(field, message string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		writeFreshJSON(w, struct {
			Code    string `json:"code"`
			Field   string `json:"field,omitempty"`
			Message string `json:"message,omitempty"`
		}{freshconfig.CodeCPARejected, field, message})
	}
	switch {
	case errors.As(e, &rejection):
		// A refusal this Manager decided before CPA was asked: the proxy-URL
		// credential rule, or a value outside the private profile's range. Only
		// a bounded Manager code reaches the wire; anything else would turn a
		// string the Manager does not own into page behaviour.
		pageCode := rejection.Code
		if !freshconfig.ManagerCode(pageCode) {
			pageCode = "invalid_config_request"
			status = http.StatusBadRequest
		} else {
			status = http.StatusUnprocessableEntity
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		writeFreshJSON(w, struct {
			Code    string `json:"code"`
			Field   string `json:"field,omitempty"`
			Message string `json:"message,omitempty"`
		}{pageCode, rejection.Field, rejection.Message})
		return
	case errors.As(e, &refusal):
		// CPA parsed, validated and refused: the file is unchanged. CPA's own
		// code is evidence, not page behaviour, so the wire code is the bounded
		// cpa_rejected_config and the field and redacted diagnostic travel with
		// it: that is what puts the refusal on the leaf the user must fix.
		replyRefusal(refusal.Field, refusal.Message)
		return
	case errors.As(e, &writeError):
		replyRefusal(writeError.Field, writeError.Message)
		return
	case errors.Is(e, freshconfig.ErrInvalid):
		code, status = "invalid_config_request", http.StatusBadRequest
	case errors.Is(e, freshconfig.ErrNotFound):
		code, status = "operation_not_found", http.StatusNotFound
	case errors.Is(e, freshconfig.ErrRulesReadOnly):
		code, status = "payload_rules_read_only", http.StatusConflict
	case errors.Is(e, freshconfig.ErrReadOnly):
		code, status = "gateway_config_read_only", http.StatusConflict
	case errors.Is(e, freshconfig.ErrGenerationMismatch):
		// A typed conflict, not a moved baseline: the draft belongs to another
		// CPA target and the page re-reads before offering the save again.
		code, status = "generation_mismatch", http.StatusConflict
	case errors.Is(e, freshconfig.ErrLegacyUnresolved):
		code, status = "legacy_operation_unresolved", http.StatusConflict
	case errors.Is(e, freshconfig.ErrPluginAuthority):
		// The plugin switch is the managed authority's alone. The page hides the
		// control instead of offering a write another profile would refuse.
		code, status = freshconfig.ReasonPluginManagedOnly, http.StatusConflict
	case errors.Is(e, cpa.ErrSourceConflict), errors.Is(e, freshstore.ErrConflict):
		code, status = "source_or_operation_conflict", http.StatusConflict
	case errors.Is(e, cpa.ErrProofUnavailable):
		code, status = "config_authority_unavailable", http.StatusConflict
	case errors.Is(e, cpa.ErrWriterIsolation):
		code, status = "config_key_writer_not_isolated", http.StatusConflict
	case errors.Is(e, cpa.ErrApplyUnknown):
		code, status = "original_operation_unknown", http.StatusConflict
	case errors.Is(e, cpa.ErrManagementRejected):
		// The user's CPA refused the stored management credential. Only rebinding
		// the connection resolves it, so the page shows that instead of a retry.
		code, status = freshconfig.ReasonManagementRejected, http.StatusConflict
	case errors.Is(e, cpa.ErrExternalTargetMoved):
		// The committed target and the live connection name different CPAs. The
		// authority is resolved before any read or write, so nothing was written;
		// this is the same typed blocker the read publishes as capability.reason,
		// not an unavailable authority and not a retry reason.
		code, status = freshconfig.ReasonTargetMoved, http.StatusConflict
	case errors.Is(e, cpa.ErrExternalVersionUnsupported):
		// The user's CPA did not prove the v8 contract this Manager writes, so no
		// journal entry and no write exist. The page keeps reading that CPA; only
		// the save is refused.
		code, status = freshconfig.ReasonVersionUnsupported, http.StatusConflict
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeFreshJSON(w, struct {
		Code   string `json:"code"`
		Result any    `json:"result,omitempty"`
	}{code, v})
}

// gatewayConfiguration serves the gateway configuration page:
//
//	GET  gateway/config
//	POST gateway/config/groups/{groupId}
//	POST gateway/config/rules/{family}
//	GET  gateway/config/operations/{requestId}
//	POST gateway/config/operations/{requestId}/reconcile
//
// The canonical Public API has already verified the persisted Administrator
// credential. No supplied URL, key, proof flag, YAML or recovery bytes exist, and
// a resource outside this surface is a 404 that never reaches CPA.
func (a *freshApplication) gatewayConfiguration(w http.ResponseWriter, r *http.Request, resource string) {
	if r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// Request shape is validated before any profile question: an invalid body is
	// a 400 whether or not this installation holds a configuration authority.
	parts := strings.Split(resource, "/")
	switch {
	case resource == "":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		service := a.gatewayService(w)
		if service == nil {
			return
		}
		// A key the user's CPA already refused is not presented again: this is
		// the config page's own read, it polls, and five refusals ban the address.
		grant := a.externalReadGrant(r.Context())
		if grant.Refused {
			gatewayConfigReply(w, freshconfig.View{}, cpa.ErrManagementRejected)
			return
		}
		view, err := service.Read(r.Context())
		if errors.Is(err, cpa.ErrManagementRejected) {
			a.noteExternalRejection(r.Context(), grant, err)
		}
		gatewayConfigReply(w, view, err)
	case parts[0] == "groups" && len(parts) == 2:
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		body, ok := gatewayConfigBody(w, r)
		if !ok {
			return
		}
		if !exactObject(body, "requestId", "expected", "edits") || !exactExpected(body) {
			gatewayConfigReply(w, nil, freshconfig.ErrInvalid)
			return
		}
		var q freshconfig.GroupRequest
		if json.Unmarshal(body, &q) != nil {
			gatewayConfigReply(w, nil, freshconfig.ErrInvalid)
			return
		}
		if err := freshconfig.ValidateGroupRequest(parts[1], q); err != nil {
			// The one rule this Manager decides while validating the shape is a
			// proxy URL carrying credentials: it names the field the user must
			// fix, so it is a refusal, not a malformed request.
			gatewayConfigReply(w, nil, err)
			return
		}
		service := a.gatewayService(w)
		if service == nil {
			return
		}
		operation, err := service.SaveGroup(r.Context(), parts[1], q)
		gatewayConfigReply(w, operation, err)
	case parts[0] == "rules" && len(parts) == 2:
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		body, ok := gatewayConfigBody(w, r)
		if !ok {
			return
		}
		if !exactObject(body, "requestId", "expected", "rules") || !exactExpected(body) {
			gatewayConfigReply(w, nil, freshconfig.ErrInvalid)
			return
		}
		var q freshconfig.FamilyRequest
		if json.Unmarshal(body, &q) != nil || freshconfig.ValidateFamilyRequest(parts[1], q) != nil {
			gatewayConfigReply(w, nil, freshconfig.ErrInvalid)
			return
		}
		service := a.gatewayService(w)
		if service == nil {
			return
		}
		operation, err := service.SaveFamily(r.Context(), parts[1], q)
		gatewayConfigReply(w, operation, err)
	case parts[0] == "operations" && (len(parts) == 2 || len(parts) == 3 && parts[2] == "reconcile"):
		a.gatewayConfigOperation(w, r, parts)
	default:
		http.NotFound(w, r)
	}
}

// gatewayConfigOperation inspects the ORIGINAL operation only. Reconcile asks the
// source for its record and never repeats the write.
func (a *freshApplication) gatewayConfigOperation(w http.ResponseWriter, r *http.Request, parts []string) {
	reconcile := len(parts) == 3
	method := http.MethodGet
	if reconcile {
		method = http.MethodPost
	}
	if r.Method != method {
		methodNotAllowed(w, method)
		return
	}
	if reconcile {
		b, ok := gatewayConfigBody(w, r)
		if !ok {
			return
		}
		if !exactObject(b) {
			gatewayConfigReply(w, nil, freshconfig.ErrInvalid)
			return
		}
	}
	service := a.gatewayService(w)
	if service == nil {
		return
	}
	operation, err := service.Operation(r.Context(), parts[1], reconcile)
	gatewayConfigReply(w, operation, err)
}

// gatewayService reports this installation's page service, or writes the closed
// refusal once.
func (a *freshApplication) gatewayService(w http.ResponseWriter) *freshconfig.Service {
	if a.gatewayConfig == nil {
		gatewayConfigReply(w, nil, cpa.ErrProofUnavailable)
		return nil
	}
	return a.gatewayConfig
}

func methodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	w.WriteHeader(http.StatusMethodNotAllowed)
}

// exactExpected proves the nested expectation object is closed as well: a group
// save may not smuggle a source hash or a revision CPA does not accept.
func exactExpected(body []byte) bool {
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return false
	}
	return exactObject(raw["expected"], "generation", "baseline")
}
