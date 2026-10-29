package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
)

// Called only after canonical routing and the existing Admin verifier succeed.
func (a *freshApplication) clientKeys(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation, resource string) {
	// Staging is available while the installation has not selected a strategy.
	// Once one is committed, the installation authority answers for it: New
	// Managed, the private profile and External each answer through their own
	// profile, so a key route can never accept a checkpoint the surface
	// discriminator refuses.
	if !op.GatewayPending {
		w.WriteHeader(http.StatusConflict)
		return
	}
	// The staging routes (list, create, rename) are Manager-local and existed
	// before any CPA authority did, so they keep serving a New Managed
	// checkpoint exactly as before. Every other strategy must be admitted by the
	// installation authority: External answers through its own committed
	// connection, and an adopted or incomplete checkpoint answers nothing.
	if op.Strategy.Strategy != "" && op.Strategy.Strategy != bootstrap.NewManaged &&
		(a.keyApply == nil || !a.keyApply.Targets(op, freshkeyapply.SurfaceKeyOperation)) {
		w.WriteHeader(http.StatusConflict)
		return
	}
	switch {
	case strings.HasPrefix(resource, "api-keys/operations/"):
		a.clientKeyOperation(w, r, op, resource)
	case resource == "api-keys/usage-summary" && r.Method == http.MethodGet:
		a.keyUsageSummary(w, r, op)
	case strings.HasSuffix(resource, "/usage") && strings.Count(resource, "/") == 2 && r.Method == http.MethodGet:
		a.keyUsage(w, r, op, strings.TrimSuffix(strings.TrimPrefix(resource, "api-keys/"), "/usage"))
	case strings.HasSuffix(resource, "/revocation") && strings.Count(resource, "/") == 2:
		a.prepareClientKeyOperation(w, r, op, strings.TrimSuffix(strings.TrimPrefix(resource, "api-keys/"), "/revocation"))
	case strings.HasSuffix(resource, "/secret") && strings.Count(resource, "/") == 2 && r.Method == http.MethodPost:
		a.clientKeySecret(w, r, op, strings.TrimSuffix(strings.TrimPrefix(resource, "api-keys/"), "/secret"))
	case strings.HasSuffix(resource, "/initial-install") && strings.Count(resource, "/") == 2:
		a.initialClientKey(w, r, op, strings.TrimSuffix(strings.TrimPrefix(resource, "api-keys/"), "/initial-install"))
	case resource == "api-keys" && r.Method == http.MethodGet:
		keys, err := a.keys.List(r.Context())
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// An External installation reports the presence the user's CPA answers,
		// not the effect this Manager intended. Any other profile is unchanged, and
		// a checkpoint the External discriminator refuses is never contacted.
		unverified := false
		if a.keyApply != nil && a.keyApply.Profile() == freshkeyapply.ProfileExternal &&
			a.keyApply.Targets(op, freshkeyapply.SurfaceKeyOperation) {
			// A credential the user's CPA already refused is not presented again:
			// five failed authentications ban the address for thirty minutes, and
			// a page that refreshes is enough to reach that. The blocker is
			// answered here, before any request leaves this process.
			grant := a.externalReadGrant(r.Context())
			if grant.Refused {
				writeExternalManagementRejected(w)
				return
			}
			var listErr error
			keys, unverified, listErr = a.keyApply.ExternalKeyList(r.Context(), keys)
			switch {
			case errors.Is(listErr, cpa.ErrExternalTargetMoved):
				// The connection no longer names the committed target. The staged
				// keys are not an effect of any CPA, so the list is a typed blocker
				// rather than a stale 200.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				writeFreshJSON(w, struct {
					Code string `json:"code"`
				}{freshconfig.ReasonTargetMoved})
				return
			case errors.Is(listErr, cpa.ErrManagementRejected):
				// The user's CPA refused the stored management credential. The
				// page must not offer a retry: only a rebind resolves it. The
				// refusal is remembered, so no later read sends the key again.
				a.noteExternalRejection(r.Context(), grant, listErr)
				writeExternalManagementRejected(w)
				return
			case listErr != nil:
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		writeFreshJSON(w, struct {
			Keys                []freshkeys.Key `json:"keys"`
			GatewayPending      bool            `json:"gatewayPending"`
			RevocationAvailable bool            `json:"revocationAvailable"`
			// PresenceUnverified marks a list the user's CPA could not be read
			// for: the keys are the Manager's own records, and none of them
			// claims an observed effect.
			PresenceUnverified bool `json:"presenceUnverified"`
		}{keys, true, a.keyApply.KeyOperationsEnabled(), unverified})
	case strings.HasPrefix(resource, "api-keys/requests/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(resource, "api-keys/requests/")
		key, err := a.keys.Lookup(r.Context(), id)
		if errors.Is(err, freshkeys.ErrRequest) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if errors.Is(err, freshkeys.ErrNotFound) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeFreshJSON(w, key)
	case strings.HasPrefix(resource, "api-keys/") && strings.Count(resource, "/") == 1 && (r.Method == http.MethodGet || r.Method == http.MethodPut):
		id := strings.TrimPrefix(resource, "api-keys/")
		var key freshkeys.Key
		var err error
		if r.Method == http.MethodGet {
			key, err = a.keys.Detail(r.Context(), id)
		} else {
			if r.Header.Get("Content-Type") != "application/json" {
				w.WriteHeader(http.StatusUnsupportedMediaType)
				return
			}
			var request struct {
				Name         *string `json:"name"`
				NameRevision *int64  `json:"nameRevision"`
			}
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
			d.DisallowUnknownFields()
			var trailing any
			if d.Decode(&request) != nil || request.Name == nil || request.NameRevision == nil || d.Decode(&trailing) != io.EOF {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			key, err = a.keys.Rename(r.Context(), id, *request.Name, *request.NameRevision)
		}
		switch {
		case errors.Is(err, freshkeys.ErrRequest):
			w.WriteHeader(http.StatusBadRequest)
		case errors.Is(err, freshkeys.ErrNotFound):
			w.WriteHeader(http.StatusNotFound)
		case errors.Is(err, freshkeys.ErrConflict):
			w.WriteHeader(http.StatusConflict)
		case err != nil:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			writeFreshJSON(w, key)
		}
	case resource == "api-keys" && r.Method == http.MethodPost:
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		var request struct {
			RequestID string `json:"requestId"`
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		d.DisallowUnknownFields()
		if d.Decode(&request) != nil || !freshkeys.ValidRequestID(request.RequestID) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var trailing any
		if d.Decode(&trailing) != io.EOF {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		result, created, err := a.keys.Create(r.Context(), request.RequestID)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if created {
			w.WriteHeader(http.StatusCreated)
			if a.usageProjection != nil {
				a.usageProjection.Wake()
			}
		}
		writeFreshJSON(w, result)
	default:
		// Including the final staged key: no delete/replace route exists, and a
		// saved secret is never part of a list or detail response.
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// clientKeySecret is the authenticated on-demand copy of one saved Client Key.
// The interaction is fixed: lists and details show only the masked preview, and
// the full value is decrypted for this response alone. It is never logged,
// journaled, cached or stored in the browser.
func (a *freshApplication) clientKeySecret(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation, id string) {
	if a.keys == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	var body *struct{}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128))
	d.DisallowUnknownFields()
	var trailing any
	if d.Decode(&body) != nil || body == nil || d.Decode(&trailing) != io.EOF {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	key, err := a.keys.Detail(r.Context(), id)
	if errors.Is(err, freshkeys.ErrRequest) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if errors.Is(err, freshkeys.ErrNotFound) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	// A superseded record is retained history: it can no longer authenticate, so
	// there is nothing to copy.
	if key.Lifecycle != identity.LifecycleActive {
		w.WriteHeader(http.StatusConflict)
		return
	}
	var secret string
	if err := a.keys.WithStagedSecret(r.Context(), id, func(value string) error {
		secret = value
		return nil
	}); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	writeFreshJSON(w, struct {
		Secret string `json:"secret"`
	}{secret})
}
