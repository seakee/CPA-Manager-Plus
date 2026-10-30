package main

import (
	"encoding/json"
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
)

// This is a v2-only Administrator resource, never a continuation/setup write.
func (a *freshApplication) providerNames(w http.ResponseWriter, r *http.Request, complete bool) {
	w.Header().Set("Cache-Control", "no-store")
	if !complete {
		w.WriteHeader(http.StatusPreconditionRequired)
		return
	}
	if r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		methodNotAllowed(w, "GET, PUT")
		return
	}
	service := a.gatewayService(w)
	if service == nil {
		return
	}
	grant := a.externalReadGrant(r.Context())
	if grant.Refused {
		w.WriteHeader(http.StatusConflict)
		return
	}
	if r.Method == http.MethodGet {
		view, err := service.ProviderNames(r.Context())
		a.noteExternalRejection(r.Context(), grant, err)
		gatewayConfigReply(w, view, err)
		return
	}
	body, ok := gatewayConfigBody(w, r)
	if !ok {
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	for _, value := range fields {
		if string(value) == "null" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	var q freshconfig.ProviderNameRequest
	if !exactObject(body, "generation", "etag", "provider", "index", "name") || json.Unmarshal(body, &q) != nil || freshconfig.ValidateProviderName(q) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	err := service.RenameProvider(r.Context(), q)
	a.noteExternalRejection(r.Context(), grant, err)
	gatewayConfigReply(w, map[string]bool{"saved": err == nil}, err)
}
