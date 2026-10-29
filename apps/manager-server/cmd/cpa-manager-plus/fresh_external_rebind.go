package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
)

// externalRebindRoute accepts a replacement management credential for the
// address this installation already committed. It is the only resolution of the
// external_management_key_rejected blocker: the user's CPA is administered by the
// user, so a rotated credential is theirs to resupply, never something this
// Manager guesses or retries.
//
//	POST external/rebind
//	{"requestId":"…","managementKey":"…"}
//
// An optional baseUrl is accepted only to refuse it: naming another address is a
// reconnect, which commits its own installation.
//
// The candidate is proven against the committed address before anything is
// persisted, so a refused credential leaves the installation exactly as it was.
func (a *freshApplication) externalRebindRoute(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation, suffix string) {
	if suffix != "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if !op.ProductComplete || a.keyApply == nil || a.protector == nil || !a.keyApply.KeyOperationsEnabled() {
		w.WriteHeader(http.StatusConflict)
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	var request struct {
		RequestID     string `json:"requestId"`
		ManagementKey string `json:"managementKey"`
		// BaseURL is refused when it names anything but the committed address. A
		// rebind re-points a credential, never the installation, so a different
		// address is a reconnect and is answered as such instead of quietly
		// reaching another CPA.
		BaseURL string `json:"baseUrl,omitempty"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF ||
		strings.TrimSpace(request.RequestID) == "" || strings.TrimSpace(request.ManagementKey) == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	store, ok := a.store.(externalRebindStore)
	if !ok {
		// A store that cannot replace a connection has no rebind to offer. It is
		// refused rather than emulated through a CAS that never rewrites one.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		writeFreshJSON(w, struct {
			Code string `json:"code"`
		}{"external_rebind_unavailable"})
		return
	}
	rebound, err := a.keyApply.Rebind(r.Context(), store, a.protector, op.Strategy.TargetGeneration,
		strings.TrimSpace(request.BaseURL), request.ManagementKey)
	if err != nil {
		a.writeExternalRebindError(w, err)
		return
	}
	// The credential was proven with one authenticated read, so whatever the CPA
	// refused before is now answered. Clearing it is what ends the local blocker:
	// the reads resume, and a re-verify leaves the installation itself untouched.
	a.clearExternalRejection(r.Context())
	// The connection changed under every cached authority, so the capabilities
	// proven against the previous credential are forgotten rather than reused. A
	// re-verify changed no credential, so it has nothing to forget.
	if !rebound.Reverified {
		a.pluginCapability.forget()
	}
	// Either outcome is the resolution of a refused credential, so collection is
	// restarted on the credential that now authenticates. The old Collector is
	// stopped explicitly: it may still be retrying the refused key, and starting a
	// second run over it would leave two pollers authenticating against a CPA that
	// bans the address after a handful of failures.
	a.stopUsageCollection(r.Context())
	a.startUsageCollection(op)
	status := "rebound"
	if rebound.Reverified {
		status = "reverified"
	}
	w.Header().Set("Cache-Control", "no-store")
	writeFreshJSON(w, struct {
		Identity   string `json:"identity"`
		Generation uint64 `json:"generation"`
		Status     string `json:"status"`
		Revision   uint64 `json:"revision"`
	}{
		Identity:   rebound.Record.Operation.Strategy.TargetIdentity,
		Generation: rebound.Record.Operation.Strategy.TargetGeneration,
		Status:     status,
		Revision:   rebound.Record.Operation.Revision,
	})
}

// externalRebindStore is the durable record a rebind needs: the Fresh CAS store
// plus the one capability that replaces a committed connection's credential.
type externalRebindStore interface {
	freshstore.Store
	freshstore.ConnectionRebinder
}

// externalRebindForbiddenDetail is the administrator-facing explanation of a
// 403 from the user's CPA. CPA answers 401 to a credential it does not hold and
// 403 to a client it refuses: an address banned after too many failed
// authentications, or remote management disabled for it. The code is the same
// rejection either way; the ban is the one refusal that a retry of the same
// credential can resolve, so it is the one that says so.
const externalRebindForbiddenDetail = "the CPA replaced this key or is refusing this client; if its address is banned, wait out the ban window and then rebind with the current key"

// externalRebindRejectedDetail is the same instruction for a 401: the candidate is
// not the key this CPA holds. Supplying the current key again is still worth
// trying when it was never rotated, because that rebind re-verifies it.
const externalRebindRejectedDetail = "the CPA does not accept this key; if it was never rotated, wait out any ban window and rebind with the current key"

// writeExternalRebindError maps a refused rebind to its typed answer. No branch
// carries the credential, the address or any text from the user's CPA.
func (a *freshApplication) writeExternalRebindError(w http.ResponseWriter, err error) {
	status, code := http.StatusServiceUnavailable, "external_rebind_unavailable"
	detail := ""
	switch {
	case errors.Is(err, cpa.ErrManagementClientForbidden):
		// CPA refused this client rather than the credential: it is a ban or a
		// disabled remote management, so the same candidate is worth one retry
		// once the window has passed. The code is unchanged; the detail is what
		// tells the page not to ask for a new key first.
		status, code, detail = http.StatusConflict, freshconfig.ReasonManagementRejected, externalRebindForbiddenDetail
	case errors.Is(err, cpa.ErrManagementRejected):
		// The candidate credential is not the one this CPA accepts. The page asks
		// for the real one — and says that re-supplying the stored one re-verifies
		// it, which is the way back when the key was never rotated.
		status, code, detail = http.StatusConflict, freshconfig.ReasonManagementRejected, externalRebindRejectedDetail
	case errors.Is(err, cpa.ErrExternalVersionUnsupported):
		status, code = http.StatusConflict, freshconfig.ReasonVersionUnsupported
	case errors.Is(err, freshkeyapply.ErrRebindRefused):
		// The request tried to reach another address, or named a credential the
		// committed installation cannot hold: a reconnect, never a rebind.
		status, code = http.StatusConflict, "external_rebind_refused"
	case errors.Is(err, freshkeyapply.ErrGeneration), errors.Is(err, freshstore.ErrConflict):
		// Something else moved the installation while the candidate was being
		// proven. The page re-reads and asks again.
		status, code = http.StatusConflict, "external_rebind_stale_generation"
	case errors.Is(err, cpa.ErrExternalTargetMoved):
		status, code = http.StatusConflict, freshconfig.ReasonTargetMoved
	case errors.Is(err, freshkeyapply.ErrExternalUnavailable):
		status, code = http.StatusConflict, "external_cpa_authority_unavailable"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeFreshJSON(w, struct {
		Code   string `json:"code"`
		Detail string `json:"detail,omitempty"`
	}{code, detail})
}
