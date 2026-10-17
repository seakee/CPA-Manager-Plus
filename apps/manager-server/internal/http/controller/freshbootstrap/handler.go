// Package freshbootstrap is the lifecycle-bounded pre-prefix carrier. It does
// not participate in normal Admin authentication or issue host handoffs.
package freshbootstrap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/routing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshrouting"
)

type Handler struct {
	Authority *fresh.Service
	Routing   *freshrouting.Service
	Current   func(context.Context) (bootstrap.FreshOperation, error)
	Shell     http.Handler
	Strategy  http.Handler
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !routing.CanonicalBootstrapRequest(r) || h.Authority == nil || h.Current == nil {
		http.NotFound(w, r)
		return
	}
	current, err := h.Current(r.Context())
	if err != nil || current.Validate() != nil || current.Routing.Committed {
		http.NotFound(w, r)
		return
	}
	switch {
	case r.URL.Path == bootstrap.FixedBootstrapDiscoveryPath:
		switch r.Method {
		case http.MethodOptions:
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			bootstrapdiscovery.Handler{Discovery: h.Authority.Discover(r.Context())}.ServeHTTP(w, r)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	case r.URL.Path == bootstrap.FixedBootstrapUIPath || strings.HasPrefix(r.URL.Path, bootstrap.FixedBootstrapAssetsPath):
		if h.Shell == nil {
			http.NotFound(w, r)
			return
		}
		h.Shell.ServeHTTP(w, r)
		return
	case !strings.HasPrefix(r.URL.Path, bootstrap.FixedBootstrapAPIPath):
		http.NotFound(w, r)
		return
	}
	resource := strings.TrimPrefix(r.URL.Path, bootstrap.FixedBootstrapAPIPath)
	if resource == "claim/exchange" {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// The exchange body carries the one-exchange claim; no other credential
		// header is accepted as a substitute for it.
		if r.Header.Get("Authorization") != "" {
			reject(w)
			return
		}
		var request fresh.ExchangeRequest
		if !decode(w, r, &request) {
			badRequest(w)
			return
		}
		result, err := h.Authority.Exchange(r.Context(), request)
		if err != nil {
			reject(w)
			return
		}
		writeJSON(w, result)
		return
	}
	known := map[string]string{"operation": http.MethodGet, "admin/generate": http.MethodPost, "admin/commit": http.MethodPost, "routing/prepare": http.MethodPost, "routing/activate": http.MethodPost, "strategy": http.MethodPost}
	method, ok := known[resource]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if method != r.Method {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token, ok := SessionToken(r)
	if !ok {
		reject(w)
		return
	}
	op, err := h.Authority.Authorize(r.Context(), token)
	if err != nil {
		reject(w)
		return
	}
	switch resource {
	case "operation":
		writeJSON(w, op)
	case "admin/generate":
		key, err := h.Authority.GenerateAdmin(r.Context(), token)
		if err != nil {
			reject(w)
			return
		}
		writeJSON(w, struct {
			Key string `json:"key"`
		}{key})
	case "admin/commit":
		var request fresh.AdminCommitRequest
		if !decode(w, r, &request) {
			badRequest(w)
			return
		}
		result, err := h.Authority.CommitAdmin(r.Context(), token, request)
		if err != nil {
			reject(w)
			return
		}
		writeJSON(w, result)
	case "routing/prepare", "routing/activate":
		if h.Routing == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var request struct {
			Identity bootstrap.FreshIdentity `json:"identity"`
			Target   string                  `json:"target,omitempty"`
		}
		if !decode(w, r, &request) || request.Identity != op.Identity {
			badRequest(w)
			return
		}
		var result bootstrap.FreshOperation
		if resource == "routing/prepare" {
			result, err = h.Routing.PrepareAuthorized(r.Context(), request.Identity, request.Target, func(record freshstore.Record) error { return h.Authority.ValidateSession(record, token) })
		} else {
			result, err = h.Routing.ActivateAuthorized(r.Context(), request.Identity, func(record freshstore.Record) error { return h.Authority.ValidateSession(record, token) })
		}
		if err != nil {
			w.WriteHeader(http.StatusConflict)
			return
		}
		writeJSON(w, result)
	case "strategy":
		if h.Strategy == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		h.Strategy.ServeHTTP(w, r)
	}
}

// SessionToken deliberately uses a distinct scheme. Bearer Admin/CPA keys and
// normal Admin cookies never enter the Fresh authority path.
func SessionToken(r *http.Request) (string, bool) {
	v := r.Header.Values("Authorization")
	if len(v) != 1 || !strings.HasPrefix(v[0], "Fresh ") {
		return "", false
	}
	token := strings.TrimPrefix(v[0], "Fresh ")
	return token, token != "" && !strings.ContainsAny(token, " \t\r\n")
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return false
	}
	var trailing any
	return d.Decode(&trailing) == io.EOF
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func reject(w http.ResponseWriter)     { w.WriteHeader(http.StatusUnauthorized) }
func badRequest(w http.ResponseWriter) { w.WriteHeader(http.StatusBadRequest) }
