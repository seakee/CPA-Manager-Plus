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
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshrouting"
)

const freshKeysGatewayAdmissionTimeout = 1500 * time.Millisecond

type clientKeyCreateResponse struct {
	Key           freshkeys.Key   `json:"key"`
	CreatedSecret string          `json:"createdSecret,omitempty"`
	Operation     *repo.Operation `json:"operation,omitempty"`
}

type clientKeyRegenerateResponse struct {
	Key           freshkeys.Key `json:"key"`
	CreatedSecret string        `json:"createdSecret,omitempty"`
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
	case strings.HasSuffix(resource, "/regenerate") && strings.Count(resource, "/") == 2:
		a.regenerateClientKey(w, r, op, authority, activeTarget, strings.TrimSuffix(strings.TrimPrefix(resource, "api-keys/"), "/regenerate"))
	case strings.HasSuffix(resource, "/secret") && strings.Count(resource, "/") == 2 && r.Method == http.MethodPost:
		a.clientKeySecret(w, r, op, authority, strings.TrimSuffix(strings.TrimPrefix(resource, "api-keys/"), "/secret"))
	case strings.HasSuffix(resource, "/initial-install") && strings.Count(resource, "/") == 2:
		a.initialClientKey(w, r, op, strings.TrimSuffix(strings.TrimPrefix(resource, "api-keys/"), "/initial-install"))
	case resource == "api-keys" && r.Method == http.MethodGet:
		type gatewayAdmissionSample struct {
			admitted bool
			stamp    freshkeyapply.ManagedProofStamp
		}
		var (
			keys        []freshkeys.Key
			unverified  bool
			listStamp   freshkeyapply.ManagedProofStamp
			listErr     error
			admitDone   chan gatewayAdmissionSample
			admitCtx    context.Context
			admitCancel context.CancelFunc
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
			targetGen := op.Strategy.TargetGeneration
			if op.Routing.Committed && freshkeyapply.ManagerOwnedStrategy(op.Strategy) {
				admitCtx, admitCancel = context.WithTimeout(r.Context(), freshKeysGatewayAdmissionTimeout)
				defer admitCancel()
				admitDone = make(chan gatewayAdmissionSample, 1)
				go func() {
					admitted, stamp := authority.DefaultInstallCPAAuthorityProvenStamp(admitCtx)
					admitDone <- gatewayAdmissionSample{admitted: admitted, stamp: stamp}
				}()
			}
			keys, unverified, listStamp, listErr = authority.ObservedKeyListStamped(r.Context(), targetGen)
			if listErr != nil {
				if admitCancel != nil {
					admitCancel()
				}
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
		switch {
		case admitDone != nil:
			var admitSample gatewayAdmissionSample
			select {
			case admitSample = <-admitDone:
			case <-admitCtx.Done():
			}
			admitCancel()
			checkCtx, checkCancel := context.WithTimeout(r.Context(), freshKeysGatewayAdmissionTimeout)
			afterOp, afterErr := a.current(checkCtx)
			checkCancel()
			beforeState := freshrouting.StateFor(op)
			afterState := freshrouting.StateFor(afterOp)
			sameGeneration := afterErr == nil &&
				afterOp.Validate() == nil &&
				afterOp.Routing.Committed &&
				afterOp.Strategy.TargetGeneration == op.Strategy.TargetGeneration &&
				afterState.Generation == beforeState.Generation &&
				afterState.InstallationID == beforeState.InstallationID
			if !unverified && sameGeneration && listStamp.Valid() {
				if admitSample.admitted && admitSample.stamp.Matches(listStamp) {
					gatewayPending = false
				} else if r.Context().Err() == nil {
					// Runtime or CPA snapshot rotated between the two parallel reads:
					// re-probe once under a strict admission deadline and require exact stamp equality.
					reCtx, reCancel := context.WithTimeout(r.Context(), freshKeysGatewayAdmissionTimeout)
					reDone := make(chan gatewayAdmissionSample, 1)
					go func() {
						reAdmitted, reStamp := authority.DefaultInstallCPAAuthorityProvenStamp(reCtx)
						reDone <- gatewayAdmissionSample{admitted: reAdmitted, stamp: reStamp}
					}()
					var reSample gatewayAdmissionSample
					select {
					case reSample = <-reDone:
					case <-reCtx.Done():
					}
					reCancel()
					gatewayPending = !(reSample.admitted && reSample.stamp.Matches(listStamp))
				}
			}
		case op.Routing.Committed && freshkeyapply.ManagerOwnedStrategy(op.Strategy):
			probeCtx, probeCancel := context.WithTimeout(r.Context(), freshKeysGatewayAdmissionTimeout)
			probeDone := make(chan gatewayAdmissionSample, 1)
			go func() {
				admitted, admitStamp := a.gatewayAdmissionProvenStamp(probeCtx)
				probeDone <- gatewayAdmissionSample{admitted: admitted, stamp: admitStamp}
			}()
			var probeSample gatewayAdmissionSample
			select {
			case probeSample = <-probeDone:
			case <-probeCtx.Done():
			}
			probeCancel()
			gatewayPending = !probeSample.admitted || unverified || (listStamp.Valid() && !probeSample.stamp.Matches(listStamp))
		case op.ProductComplete && op.Routing.Committed &&
			authority != nil && authority.Profile() == freshkeyapply.ProfileExternal &&
			authority.Targets(op, freshkeyapply.SurfaceKeyOperation):
			// External installs route model traffic directly to the user's CPA
			// rather than through CPAMP Ingress admission; readiness mirrors
			// access-info while live observation failures surface via presenceUnverified.
			gatewayPending = false
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
		if activeTarget && r.Method == http.MethodPut && authority != nil && authority.Profile() == freshkeyapply.ProfileManaged {
			committedName, committedRev := key.Name, key.NameRevision
			obsCtx, cancel := context.WithTimeout(r.Context(), freshKeysGatewayAdmissionTimeout)
			observed, obsErr := authority.ObserveKey(obsCtx, key, op.Strategy.TargetGeneration)
			cancel()
			if obsErr == nil && (observed.RuntimeState == "effective_observed" || observed.RuntimeState == "revoked_observed") {
				observed.Name, observed.NameRevision = committedName, committedRev
				key = observed
			}
		}
		writeFreshJSON(w, key)
	case resource == "api-keys" && r.Method == http.MethodPost:
		a.createAndApplyClientKey(w, r, authority, activeTarget, op.Strategy.TargetGeneration)
	default:
		writeAPIKeysMethodNotAllowed(w, resource, true)
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
		RequestID string  `json:"requestId"`
		Name      *string `json:"name"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
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
	rawName := ""
	if request.Name != nil {
		rawName = *request.Name
	}
	if _, ok := freshkeys.ValidKeyName(rawName); !ok {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	result, created, err := a.keys.CreateNamed(r.Context(), request.RequestID, rawName)
	if err != nil {
		switch {
		case errors.Is(err, freshkeys.ErrRequest):
			w.WriteHeader(http.StatusBadRequest)
		case errors.Is(err, freshkeys.ErrConflict), errors.Is(err, freshkeys.ErrRetired):
			w.WriteHeader(http.StatusConflict)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
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
		if preErr != nil {
			if errors.Is(preErr, errExternalCredentialWaitCancelled) {
				writeExternalCredentialWaitCancelled(w)
				return
			}
			switch {
			case errors.Is(preErr, errExternalRejectionUnavailable), errors.Is(preErr, cpa.ErrExternalRejectionUnavailable):
				key.EffectCode = freshconfig.ReasonUnavailable
				writeCreateKeyError(w, http.StatusServiceUnavailable, freshconfig.ReasonUnavailable, "", &key, nil)
			case errors.Is(preErr, cpa.ErrExternalTargetMoved):
				key.EffectCode = freshconfig.ReasonTargetMoved
				writeCreateKeyError(w, http.StatusConflict, freshconfig.ReasonTargetMoved, "", &key, nil)
			case errors.Is(preErr, cpa.ErrManagementClientForbidden):
				key.EffectCode = freshconfig.ReasonManagementRejected
				writeCreateKeyError(w, http.StatusConflict, freshconfig.ReasonManagementRejected, "client_forbidden", &key, nil)
			case errors.Is(preErr, cpa.ErrManagementRejected):
				key.EffectCode = freshconfig.ReasonManagementRejected
				writeCreateKeyError(w, http.StatusConflict, freshconfig.ReasonManagementRejected, "", &key, nil)
			default:
				status, code := keyOperationCode(preErr)
				key.EffectCode = code
				writeCreateKeyError(w, status, code, "", &key, nil)
			}
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

func (a *freshApplication) regenerateClientKey(
	w http.ResponseWriter,
	r *http.Request,
	op bootstrap.FreshOperation,
	authority *freshkeyapply.Service,
	activeTarget bool,
	targetID string,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !activeTarget {
		w.WriteHeader(http.StatusConflict)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	var request struct {
		RequestID string `json:"requestId"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	d.DisallowUnknownFields()
	var trailing any
	if d.Decode(&request) != nil || !freshkeys.ValidRequestID(request.RequestID) || d.Decode(&trailing) != io.EOF || !freshkeys.ValidRequestID(targetID) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if authority.Profile() == freshkeyapply.ProfileExternal {
		preErr := a.checkExternalSavedCredentialPrecheck(r.Context())
		if preErr != nil {
			if errors.Is(preErr, errExternalCredentialWaitCancelled) {
				writeExternalCredentialWaitCancelled(w)
				return
			}
			existing, detailErr := a.keys.Detail(r.Context(), targetID)
			if errors.Is(detailErr, freshkeys.ErrNotFound) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var keyPtr *freshkeys.Key
			if detailErr == nil {
				keyPtr = &existing
			}
			switch {
			case errors.Is(preErr, errExternalRejectionUnavailable):
				if keyPtr != nil {
					keyPtr.EffectCode = freshconfig.ReasonUnavailable
				}
				writeCreateKeyError(w, http.StatusServiceUnavailable, freshconfig.ReasonUnavailable, "", keyPtr, nil)
			case errors.Is(preErr, cpa.ErrManagementClientForbidden):
				if keyPtr != nil {
					keyPtr.EffectCode = freshconfig.ReasonManagementRejected
				}
				writeCreateKeyError(w, http.StatusConflict, freshconfig.ReasonManagementRejected, "client_forbidden", keyPtr, nil)
			case errors.Is(preErr, cpa.ErrManagementRejected):
				if keyPtr != nil {
					keyPtr.EffectCode = freshconfig.ReasonManagementRejected
				}
				writeCreateKeyError(w, http.StatusConflict, freshconfig.ReasonManagementRejected, "", keyPtr, nil)
			default:
				status, code := keyOperationCode(preErr)
				if keyPtr != nil {
					keyPtr.EffectCode = code
				}
				writeCreateKeyError(w, status, code, "", keyPtr, nil)
			}
			return
		}
	}

	applyCtx := r.Context()
	if authority.Profile() == freshkeyapply.ProfileExternal {
		applyCtx = a.withExternalSavedCredentialGate(applyCtx)
	}
	generation := op.Strategy.TargetGeneration

	staged, created, opRes, applyErr := authority.ApplyStagedRotate(applyCtx, request.RequestID, targetID, generation)
	if opRes.Operation == nil && applyErr != nil {
		if errors.Is(applyErr, errExternalCredentialWaitCancelled) {
			writeExternalCredentialWaitCancelled(w)
			return
		}
		switch {
		case errors.Is(applyErr, freshkeys.ErrRequest):
			w.WriteHeader(http.StatusBadRequest)
			return
		case errors.Is(applyErr, freshkeys.ErrNotFound):
			w.WriteHeader(http.StatusNotFound)
			return
		case errors.Is(applyErr, freshkeys.ErrConflict), errors.Is(applyErr, freshkeys.ErrRetired), errors.Is(applyErr, repo.ErrConflict):
			existing, detailErr := a.keys.Detail(r.Context(), targetID)
			if errors.Is(detailErr, freshkeys.ErrNotFound) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var keyPtr *freshkeys.Key
			if detailErr == nil {
				existing.EffectCode = "key_operation_conflict"
				keyPtr = &existing
			}
			var resPtr *freshkeyapply.OperationResult
			if a.keyRepo != nil {
				if latest, latestErr := a.keyRepo.LatestOperation(r.Context()); latestErr == nil && latest != nil && !latest.IsTerminal() {
					conflictRes := freshkeyapply.OperationResult{Operation: latest, RuntimeState: "effect_unknown"}
					if keyPtr != nil {
						applyRotateErrorKeyState(keyPtr, conflictRes)
						keyPtr.EffectCode = "key_operation_conflict"
					}
					resPtr = &conflictRes
				}
			}
			writeCreateKeyError(w, http.StatusConflict, "key_operation_conflict", "", keyPtr, resPtr)
			return
		default:
			existing, detailErr := a.keys.Detail(r.Context(), targetID)
			if errors.Is(detailErr, freshkeys.ErrNotFound) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var keyPtr *freshkeys.Key
			if detailErr == nil {
				keyPtr = &existing
			}
			if errors.Is(applyErr, cpa.ErrExternalRejectionUnavailable) {
				if keyPtr != nil {
					keyPtr.EffectCode = freshconfig.ReasonUnavailable
				}
				writeCreateKeyError(w, http.StatusServiceUnavailable, freshconfig.ReasonUnavailable, "", keyPtr, nil)
				return
			}
			status, code := keyOperationCode(applyErr)
			detail := ""
			if errors.Is(applyErr, cpa.ErrManagementClientForbidden) {
				detail = "client_forbidden"
			}
			if keyPtr != nil {
				keyPtr.EffectCode = code
			}
			writeCreateKeyError(w, status, code, detail, keyPtr, nil)
			return
		}
	}

	key := staged.Key
	if applyErr == nil && opRes.RuntimeState != "effective_observed" {
		applyErr = cpa.ErrApplyUnknown
	}
	if opRes.Operation != nil && opRes.Operation.Outcome == repo.OutcomeCommitted && a.usageProjection != nil {
		a.usageProjection.Wake()
	}
	if refreshed, detailErr := a.keys.Detail(r.Context(), targetID); detailErr == nil {
		key = refreshed
	}
	if applyErr != nil {
		if errors.Is(applyErr, errExternalCredentialWaitCancelled) {
			writeExternalCredentialWaitCancelled(w)
			return
		}
		applyRotateErrorKeyState(&key, opRes)
		var resPtr *freshkeyapply.OperationResult
		if opRes.Operation != nil {
			resPtr = &opRes
		}
		if errors.Is(applyErr, cpa.ErrExternalRejectionUnavailable) {
			key.EffectCode = freshconfig.ReasonUnavailable
			writeCreateKeyError(w, http.StatusServiceUnavailable, freshconfig.ReasonUnavailable, "", &key, resPtr)
			return
		}
		status, code := keyOperationCode(applyErr)
		detail := ""
		if errors.Is(applyErr, cpa.ErrManagementClientForbidden) {
			detail = "client_forbidden"
		}
		key.EffectCode = code
		writeCreateKeyError(w, status, code, detail, &key, resPtr)
		return
	}

	key.RuntimeState = "effective_observed"
	key.OperationID = ""
	key.EffectCode = ""
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	resp := clientKeyRegenerateResponse{Key: key}
	if created {
		resp.CreatedSecret = staged.NewSecret
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
func (a *freshApplication) clientKeySecret(
	w http.ResponseWriter,
	r *http.Request,
	op bootstrap.FreshOperation,
	authority *freshkeyapply.Service,
	id string,
) {
	applyCtx := r.Context()
	if authority != nil && authority.Profile() == freshkeyapply.ProfileExternal {
		applyCtx = a.withExternalSavedCredentialGate(applyCtx)
	}
	var wakeFn func()
	if a.usageProjection != nil {
		wakeFn = a.usageProjection.Wake
	}
	serveClientKeySecret(w, r, a.keys, a.keyRepo, authority, applyCtx, op.Strategy.TargetGeneration, id, wakeFn)
}

func writeSecretOperationConflict(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	writeFreshJSON(w, struct {
		Code string `json:"code"`
	}{Code: "key_operation_conflict"})
}

func serveClientKeySecret(
	w http.ResponseWriter,
	r *http.Request,
	keysSvc *freshkeys.Service,
	keyRepo *repo.Repository,
	authority *freshkeyapply.Service,
	applyCtx context.Context,
	generation uint64,
	id string,
	wakeProjection func(),
) {
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
	hasStagedRot, rotErr := keysSvc.HasStagedRotate(r.Context(), id)
	if rotErr != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	var latest *repo.Operation
	if keyRepo != nil {
		var latestErr error
		latest, latestErr = keyRepo.LatestOperation(r.Context())
		if latestErr != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
	}
	hasUnsettledRotate := hasStagedRot ||
		(latest != nil && !latest.IsTerminal() && latest.Mode == "rotate" && latest.TargetID == string(key.ID))
	if hasUnsettledRotate {
		if authority == nil {
			writeSecretOperationConflict(w)
			return
		}
		if applyCtx == nil {
			applyCtx = r.Context()
		}
		reconciled, recErr := authority.ReconcileUnsettled(applyCtx, generation)
		if errors.Is(recErr, errExternalCredentialWaitCancelled) {
			writeExternalCredentialWaitCancelled(w)
			return
		}
		if reconciled != nil && reconciled.Outcome == repo.OutcomeCommitted && wakeProjection != nil {
			wakeProjection()
		}
		stillStaged, stillStagedErr := keysSvc.HasStagedRotate(r.Context(), id)
		if recErr != nil || stillStagedErr != nil || stillStaged || (reconciled != nil && !reconciled.IsTerminal()) {
			writeSecretOperationConflict(w)
			return
		}
		key, err = keysSvc.Detail(r.Context(), id)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if key.Lifecycle == identity.LifecycleSuperseded {
			w.WriteHeader(http.StatusConflict)
			return
		}
	}
	var secret string
	if err := keysSvc.WithStagedSecret(r.Context(), id, func(value string) error {
		secret = value
		return nil
	}); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if postRot, postRotErr := keysSvc.HasStagedRotate(r.Context(), id); postRotErr != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	} else if postRot {
		writeSecretOperationConflict(w)
		return
	}
	afterKey, afterErr := keysSvc.Detail(r.Context(), id)
	if afterErr != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if afterKey.Lifecycle == identity.LifecycleSuperseded {
		w.WriteHeader(http.StatusConflict)
		return
	}
	if afterKey.LastRotatedAtMS != key.LastRotatedAtMS || afterKey.MaskedPreview != key.MaskedPreview {
		// A concurrent rotation settled between Detail and WithStagedSecret; re-read
		// once and verify no further rotation opened.
		if err := keysSvc.WithStagedSecret(r.Context(), id, func(value string) error {
			secret = value
			return nil
		}); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if postRot2, postRotErr2 := keysSvc.HasStagedRotate(r.Context(), id); postRotErr2 != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		} else if postRot2 {
			writeSecretOperationConflict(w)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	writeFreshJSON(w, struct {
		Secret string `json:"secret"`
	}{secret})
}

func writeAPIKeysMethodNotAllowed(w http.ResponseWriter, resource string, allowInitialInstall bool) {
	allow := ""
	switch {
	case resource == "api-keys":
		allow = "GET, POST"
	case resource == "api-keys/usage-summary":
		allow = "GET"
	case strings.HasPrefix(resource, "api-keys/requests/") && strings.Count(resource, "/") == 2:
		allow = "GET"
	case strings.HasSuffix(resource, "/usage") && strings.Count(resource, "/") == 2:
		allow = "GET"
	case strings.HasSuffix(resource, "/secret") && strings.Count(resource, "/") == 2:
		allow = "POST"
	case strings.HasSuffix(resource, "/regenerate") && strings.Count(resource, "/") == 2:
		allow = "POST"
	case strings.HasSuffix(resource, "/revocation") && strings.Count(resource, "/") == 2:
		allow = "POST"
	case allowInitialInstall && strings.HasSuffix(resource, "/initial-install") && strings.Count(resource, "/") == 2:
		allow = "POST"
	case (strings.Count(resource, "/") == 3 || strings.Count(resource, "/") == 4) && strings.HasPrefix(resource, "api-keys/"):
		if parts := strings.Split(resource, "/"); parts[2] == "revocation" {
			allow = "GET"
		}
	case strings.HasPrefix(resource, "api-keys/") && strings.Count(resource, "/") == 1:
		allow = "GET, PUT"
	}
	if allow != "" {
		w.Header().Set("Allow", allow)
	}
	w.WriteHeader(http.StatusMethodNotAllowed)
}
