package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
)

const freshKeysGatewayAdmissionTimeout = 1500 * time.Millisecond

type clientKeyCreateResponse struct {
	Key           freshkeys.Key   `json:"key"`
	CreatedSecret string          `json:"createdSecret,omitempty"`
	Operation     *repo.Operation `json:"operation,omitempty"`
}

type clientKeyCreateErrorResponse struct {
	Code   string                         `json:"code"`
	Detail string                         `json:"detail,omitempty"`
	Key    *freshkeys.Key                 `json:"key,omitempty"`
	Result *freshkeyapply.OperationResult `json:"result,omitempty"`
}

// Called only after canonical routing and the existing Admin verifier succeed.
func (a *freshApplication) clientKeys(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation, resource string) {
	if !op.GatewayPending {
		w.WriteHeader(http.StatusConflict)
		return
	}
	authority := a.keyApply.ForOperation(op)
	if op.Strategy.Strategy != "" && !freshkeyapply.ManagerOwnedStrategy(op.Strategy) &&
		(authority == nil || !authority.Targets(op, freshkeyapply.SurfaceKeyOperation)) {
		w.WriteHeader(http.StatusConflict)
		return
	}
	activeTarget := authority != nil && authority.KeyOperationsEnabled() &&
		authority.Targets(op, freshkeyapply.SurfaceKeyOperation) &&
		authority.Profile() != freshkeyapply.ProfilePrivate

	switch {
	case strings.HasPrefix(resource, "api-keys/operations/"):
		a.clientKeyOperation(w, r, op, resource)
	case resource == "api-keys/usage-summary" && r.Method == http.MethodGet:
		a.keyUsageSummary(w, r, op)
	case strings.HasSuffix(resource, "/usage") && strings.Count(resource, "/") == 2 && r.Method == http.MethodGet:
		a.keyUsage(w, r, op, strings.TrimSuffix(strings.TrimPrefix(resource, "api-keys/"), "/usage"))
	case r.Method == http.MethodGet && strings.Count(resource, "/") == 4:
		if parts := strings.Split(resource, "/"); parts[0] == "api-keys" && parts[2] == "revocation" {
			a.preflightClientKeyOperation(w, r, op, parts[1], parts[3], parts[4])
			return
		}
		w.WriteHeader(http.StatusNotFound)
	case r.Method == http.MethodGet && strings.Count(resource, "/") == 3:
		if parts := strings.Split(resource, "/"); parts[0] == "api-keys" && parts[2] == "revocation" {
			a.preflightClientKeyOperation(w, r, op, parts[1], "", parts[3])
			return
		}
		w.WriteHeader(http.StatusNotFound)
	case strings.HasSuffix(resource, "/revocation") && strings.Count(resource, "/") == 2:
		a.prepareClientKeyOperation(w, r, op, strings.TrimSuffix(strings.TrimPrefix(resource, "api-keys/"), "/revocation"))
	case strings.HasSuffix(resource, "/secret") && strings.Count(resource, "/") == 2 && r.Method == http.MethodPost:
		a.clientKeySecret(w, r, op, strings.TrimSuffix(strings.TrimPrefix(resource, "api-keys/"), "/secret"))
	case strings.HasSuffix(resource, "/initial-install") && strings.Count(resource, "/") == 2:
		a.initialClientKey(w, r, op, strings.TrimSuffix(strings.TrimPrefix(resource, "api-keys/"), "/initial-install"))
	case resource == "api-keys" && r.Method == http.MethodGet:
		var (
			keys       []freshkeys.Key
			unverified bool
			listErr    error
		)
		switch {
		case authority != nil && authority.Profile() == freshkeyapply.ProfileExternal &&
			authority.Targets(op, freshkeyapply.SurfaceKeyOperation):
			if a.writeExternalSavedCredentialPrecheck(w, r.Context(), func() {
				writeExternalManagementRejected(w)
			}) {
				return
			}
			keys, unverified, listErr = authority.ObservedKeyList(a.withExternalSavedCredentialGate(r.Context()), op.Strategy.TargetGeneration)
			switch {
			case errors.Is(listErr, errExternalCredentialWaitCancelled):
				writeExternalCredentialWaitCancelled(w)
				return
			case errors.Is(listErr, cpa.ErrExternalRejectionUnavailable):
				writeExternalAuthorityUnavailable(w)
				return
			case errors.Is(listErr, cpa.ErrExternalTargetMoved):
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				writeFreshJSON(w, struct {
					Code string `json:"code"`
				}{freshconfig.ReasonTargetMoved})
				return
			case errors.Is(listErr, cpa.ErrManagementClientForbidden):
				writeExternalManagementForbidden(w)
				return
			case errors.Is(listErr, cpa.ErrManagementRejected):
				writeExternalManagementRejected(w)
				return
			case listErr != nil:
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		case authority != nil && authority.Profile() == freshkeyapply.ProfileManaged &&
			authority.Targets(op, freshkeyapply.SurfaceKeyOperation):
			keys, unverified, listErr = authority.ObservedKeyList(r.Context(), op.Strategy.TargetGeneration)
			if listErr != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		default:
			keys, listErr = a.keys.List(r.Context())
			if listErr != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		gatewayPending := true
		if op.Routing.Committed && freshkeyapply.ManagerOwnedStrategy(op.Strategy) {
			admitCtx, cancel := context.WithTimeout(r.Context(), freshKeysGatewayAdmissionTimeout)
			gatewayPending = !a.gatewayAdmissionProven(admitCtx)
			cancel()
		}
		writeFreshJSON(w, struct {
			Keys                []freshkeys.Key `json:"keys"`
			GatewayPending      bool            `json:"gatewayPending"`
			RevocationAvailable bool            `json:"revocationAvailable"`
			PresenceUnverified  bool            `json:"presenceUnverified"`
		}{keys, gatewayPending, authority.KeyOperationsEnabled(), unverified})
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
		if activeTarget {
			key, ok := a.observeKeyForRead(w, r.Context(), authority, key, op.Strategy.TargetGeneration)
			if !ok {
				return
			}
			writeFreshJSON(w, key)
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
			return
		case errors.Is(err, freshkeys.ErrNotFound):
			w.WriteHeader(http.StatusNotFound)
			return
		case errors.Is(err, freshkeys.ErrConflict):
			w.WriteHeader(http.StatusConflict)
			return
		case err != nil:
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if activeTarget && r.Method == http.MethodGet {
			observed, ok := a.observeKeyForRead(w, r.Context(), authority, key, op.Strategy.TargetGeneration)
			if !ok {
				return
			}
			key = observed
		}
		writeFreshJSON(w, key)
	case resource == "api-keys" && r.Method == http.MethodPost:
		a.createAndApplyClientKey(w, r, authority, activeTarget, op.Strategy.TargetGeneration)
	default:
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (a *freshApplication) observeKeyForRead(
	w http.ResponseWriter,
	ctx context.Context,
	authority *freshkeyapply.Service,
	key freshkeys.Key,
	generation uint64,
) (freshkeys.Key, bool) {
	if authority == nil {
		return key, true
	}
	var err error
	if authority.Profile() == freshkeyapply.ProfileExternal {
		err = a.checkExternalSavedCredentialPrecheck(ctx)
		ctx = a.withExternalSavedCredentialGate(ctx)
	}
	observed := key
	if err == nil {
		observed, err = authority.ObserveKey(ctx, key, generation)
	}
	if err == nil {
		return observed, true
	}
	switch {
	case errors.Is(err, errExternalCredentialWaitCancelled):
		writeExternalCredentialWaitCancelled(w)
	case errors.Is(err, errExternalRejectionUnavailable), errors.Is(err, cpa.ErrExternalRejectionUnavailable):
		writeExternalAuthorityUnavailable(w)
	case errors.Is(err, cpa.ErrManagementClientForbidden):
		writeExternalManagementForbidden(w)
	case errors.Is(err, cpa.ErrManagementRejected):
		writeExternalManagementRejected(w)
	default:
		status, code := keyOperationCode(err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		writeFreshJSON(w, struct {
			Code string `json:"code"`
		}{code})
	}
	return key, false
}

func (a *freshApplication) createAndApplyClientKey(
	w http.ResponseWriter,
	r *http.Request,
	authority *freshkeyapply.Service,
	activeTarget bool,
	generation uint64,
) {
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
	if created && a.usageProjection != nil {
		a.usageProjection.Wake()
	}
	if !activeTarget {
		w.Header().Set("Content-Type", "application/json")
		if created {
			w.WriteHeader(http.StatusCreated)
		}
		writeFreshJSON(w, result)
		return
	}

	key := result.Key
	if authority.Profile() == freshkeyapply.ProfileExternal {
		preErr := a.checkExternalSavedCredentialPrecheck(r.Context())
		switch {
		case errors.Is(preErr, errExternalCredentialWaitCancelled):
			writeExternalCredentialWaitCancelled(w)
			return
		case errors.Is(preErr, errExternalRejectionUnavailable):
			writeCreateKeyError(w, http.StatusServiceUnavailable, freshconfig.ReasonUnavailable, "", &key, nil)
			return
		case errors.Is(preErr, cpa.ErrManagementClientForbidden):
			writeCreateKeyError(w, http.StatusConflict, freshconfig.ReasonManagementRejected, "client_forbidden", &key, nil)
			return
		case errors.Is(preErr, cpa.ErrManagementRejected):
			writeCreateKeyError(w, http.StatusConflict, freshconfig.ReasonManagementRejected, "", &key, nil)
			return
		}
	}

	applyCtx := r.Context()
	if authority.Profile() == freshkeyapply.ProfileExternal {
		applyCtx = a.withExternalSavedCredentialGate(applyCtx)
	}
	opRes, applyErr := authority.ApplyAdd(applyCtx, request.RequestID, string(key.ID), generation)
	if applyErr == nil && opRes.RuntimeState != "effective_observed" {
		applyErr = cpa.ErrApplyUnknown
	}
	if refreshed, detailErr := a.keys.Detail(r.Context(), string(key.ID)); detailErr == nil {
		key = refreshed
	}
	if applyErr != nil {
		if errors.Is(applyErr, errExternalCredentialWaitCancelled) {
			writeExternalCredentialWaitCancelled(w)
			return
		}
		if errors.Is(applyErr, cpa.ErrExternalRejectionUnavailable) {
			writeCreateKeyError(w, http.StatusServiceUnavailable, freshconfig.ReasonUnavailable, "", &key, &opRes)
			return
		}
		status, code := keyOperationCode(applyErr)
		detail := ""
		if errors.Is(applyErr, cpa.ErrManagementClientForbidden) {
			detail = "client_forbidden"
		}
		if opRes.Operation != nil && opRes.Operation.TargetID == string(key.ID) {
			if !opRes.Operation.IsTerminal() {
				key.OperationID = opRes.Operation.ID
			}
			switch opRes.Operation.Outcome {
			case "pending":
				key.RuntimeState = "applying"
			case "unknown":
				key.RuntimeState = "effect_unknown"
			case "aborted":
				key.RuntimeState = "not_applied"
				key.OperationID = ""
			}
			if opRes.Operation.Outcome == "committed" {
				key.RuntimeState = opRes.RuntimeState
			}
		}
		key.EffectCode = code
		var resPtr *freshkeyapply.OperationResult
		if opRes.Operation != nil {
			resPtr = &opRes
		}
		writeCreateKeyError(w, status, code, detail, &key, resPtr)
		return
	}

	key.RuntimeState = "effective_observed"
	key.OperationID = ""
	key.EffectCode = ""
	w.Header().Set("Content-Type", "application/json")
	if created {
		w.WriteHeader(http.StatusCreated)
	}
	resp := clientKeyCreateResponse{
		Key:       key,
		Operation: opRes.Operation,
	}
	if created {
		resp.CreatedSecret = result.CreatedSecret
	}
	writeFreshJSON(w, resp)
}

func writeCreateKeyError(
	w http.ResponseWriter,
	status int,
	code, detail string,
	key *freshkeys.Key,
	result *freshkeyapply.OperationResult,
) {
	for _, name := range proxiedEntityHeaders {
		w.Header().Del(name)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeFreshJSON(w, clientKeyCreateErrorResponse{
		Code:   code,
		Detail: detail,
		Key:    key,
		Result: result,
	})
}

// clientKeySecret is the authenticated on-demand copy of one saved Client Key.
func (a *freshApplication) clientKeySecret(w http.ResponseWriter, r *http.Request, _ bootstrap.FreshOperation, id string) {
	serveClientKeySecret(w, r, a.keys, id)
}

func serveClientKeySecret(w http.ResponseWriter, r *http.Request, keysSvc *freshkeys.Service, id string) {
	if keysSvc == nil {
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
	key, err := keysSvc.Detail(r.Context(), id)
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
	if key.Lifecycle == identity.LifecycleSuperseded {
		w.WriteHeader(http.StatusConflict)
		return
	}
	var secret string
	if err := keysSvc.WithStagedSecret(r.Context(), id, func(value string) error {
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
