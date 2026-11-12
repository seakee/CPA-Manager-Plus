package main

import (
	"context"
	"io"
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshrouting"
)

// Gateway admission is the Manager half of the private Ingress contract.
// Ingress never treats routing ownership as Gateway safety: a committed routing
// projection proves who owns Control, while admission proves that the
// destination accepts authenticated clients with an effective managed
// configuration. The answer is computed per read and never persisted, so a
// revoked key, a drifted destination or a restarted Manager withdraws it.

// freshGatewayAdmission is the non-secret admission posture. It is bound to one
// Fresh installation generation and carries no key, management secret or
// destination address.
type freshGatewayAdmission struct {
	InstallationID string `json:"installationId"`
	Generation     string `json:"generation"`
	Admitted       bool   `json:"admitted"`
}

// freshAuthorityReply repeats the one durable routing projection and adds the
// current admission posture. Ingress rejects the reply when either part is
// absent or inconsistent with its own in-memory projection.
type freshAuthorityReply struct {
	freshrouting.State
	GatewayAdmission freshGatewayAdmission `json:"gatewayAdmission"`
}

// freshAuthority is the private Manager authority consumed by Ingress. It is
// reachable only on the private shared Host namespace; public Ingress denies the
// path before any proxy and never forwards it to either upstream.
func (a *freshApplication) freshAuthority(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	op, err := a.current(r.Context())
	if err != nil || op.Validate() != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if op.Routing.Target == "" || op.Routing.OperationID == "" {
		if !op.Routing.Committed && bootstrapShellServing(r.Context(), a.fixed) {
			writePreCommitBootstrapAuthority(w)
			return
		}
		http.NotFound(w, r)
		return
	}
	admitted := false
	if op.Routing.Committed {
		// The install-derived predicate re-reads the destination on every call,
		// so admission can never be inherited from Manager memory.
		admitted = a.gatewayAdmissionProven(r.Context())
	}
	writeFreshJSON(w, freshAuthorityReplyFor(op, admitted))
}

// gatewayAdmissionProven consults the install-derived CPA authority. Both
// Manager-owned strategies (New Managed and Adopt) run under ProfileManaged and
// are admitted through their destination posture; Connect External does not
// route model traffic through CPAMP Ingress, and private attested or incomplete
// installations fail closed.
func (a *freshApplication) gatewayAdmissionProven(ctx context.Context) bool {
	if a == nil || a.keyApply == nil {
		return false
	}
	return a.keyApply.DefaultInstallCPAAuthorityProven(ctx)
}

// freshAuthorityReplyFor binds the posture to the checkpoint it was computed
// from. Admission is possible only for a committed installation.
func freshAuthorityReplyFor(op bootstrap.FreshOperation, admitted bool) freshAuthorityReply {
	state := freshrouting.StateFor(op)
	return freshAuthorityReply{
		State: state,
		GatewayAdmission: freshGatewayAdmission{
			InstallationID: state.InstallationID,
			Generation:     state.Generation,
			Admitted:       admitted && op.Routing.Committed,
		},
	}
}

func writePreCommitBootstrapAuthority(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, "{\"bootstrapUI\":true}\n")
}

type statusProbeWriter struct {
	header http.Header
	code   int
}

func (w *statusProbeWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header, 4)
	}
	return w.header
}

func (w *statusProbeWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
}

func (w *statusProbeWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return len(b), nil
}

func bootstrapShellServing(ctx context.Context, handler http.Handler) bool {
	if handler == nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, bootstrap.FixedBootstrapUIPath, nil)
	if err != nil {
		return false
	}
	var probe statusProbeWriter
	handler.ServeHTTP(&probe, req)
	return probe.code == http.StatusOK
}
