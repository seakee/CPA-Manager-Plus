package main

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	proxysvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/proxy"
)

// consoleModelListUnavailable is the stable code of a console model list this
// installation could not read at all: no CPA authority, or no readable client
// key list. It is a Manager-side statement, never a verdict on the session.
const consoleModelListUnavailable = "models_unavailable"

// errConsoleModelListUnavailable is the local failure of that read. It carries
// no CPA address and no credential: nothing here may reach a browser or a log.
var errConsoleModelListUnavailable = errors.New("console model list unavailable")

// consoleModelListResource reports whether a bridged console resource is the
// model list. The gateway serves two spellings of it, and the console asks for
// the same list either way.
func consoleModelListResource(resource, method string) bool {
	if method != http.MethodGet {
		return false
	}
	switch strings.TrimRight(resource, "/") {
	case "v1/models", "models":
		return true
	default:
		return false
	}
}

// consoleModelList answers the authenticated console's model list from the CPA
// this installation is connected to, presenting a client key that CPA already
// holds.
//
// The caller authenticated with the Manager Admin credential. A CPA does not
// accept that credential as a gateway key, so forwarding the caller's own header
// is exactly the 401 this read replaces: the list is read server-side with a key
// the CPA itself carries, and the caller's credential is never presented
// upstream.
//
// Nothing about the client key is returned or logged: the browser receives the
// CPA's own list, and a CPA that holds no client key gets the empty list this
// console already renders instead of an authentication failure it cannot fix.
func (a *freshApplication) consoleModelList(w http.ResponseWriter, r *http.Request) {
	if a == nil {
		writeConsoleModelListUnavailable(w)
		return
	}
	if a.store != nil {
		record, err := a.store.Load(r.Context())
		if err != nil {
			writeConsoleModelListUnavailable(w)
			return
		}
		if record.Operation.Strategy.Strategy == bootstrap.ConnectExternal {
			a.externalConsoleModelList(w, r, record)
			return
		}
	}
	baseURL, managementKey, err := a.consoleCPADestination(r.Context())
	if err != nil {
		writeConsoleModelListUnavailable(w)
		return
	}
	clientKey, err := consoleCPAClientKey(r.Context(), baseURL, managementKey)
	if err != nil {
		writeConsoleModelListUnavailable(w)
		return
	}
	if clientKey == "" {
		// A CPA with no client key has nothing this console may present, and the
		// Admin credential is not a gateway credential. The empty list is the
		// answer the System page renders as "no models".
		writeConsoleModelListEmpty(w)
		return
	}
	proxysvc.ProxyModelListWithClientKey(w, r, baseURL, clientKey, response.Error, response.MethodNotAllowed)
}

// externalConsoleModelList serializes the External management-key read through
// legacyCredentialGate and records a 401/403 against externalReadGrant so page
// loads never trip CPA's five-strike authentication lockout.
func (a *freshApplication) externalConsoleModelList(w http.ResponseWriter, r *http.Request, initial freshstore.Record) {
	if initial.Operation.Strategy.Stage != "committed" || a.externalSource == nil {
		writeConsoleModelListUnavailable(w)
		return
	}
	ctx := r.Context()
	gate := &a.legacyCredentialGate
	if !gate.acquire(ctx) {
		writeExternalCredentialWaitCancelled(w)
		return
	}
	record, err := a.store.Load(ctx)
	if err != nil || record.Operation.Strategy.Strategy != bootstrap.ConnectExternal || record.Operation.Strategy.Stage != "committed" {
		gate.release()
		writeConsoleModelListUnavailable(w)
		return
	}
	grant := a.externalReadGrantFor(ctx, record)
	if grant.Refused {
		gate.release()
		writeExternalManagementRejected(w)
		return
	}
	install, err := a.externalSource(ctx)
	if err != nil || install.BaseURL == "" || install.ManagementKey == "" {
		gate.release()
		writeConsoleModelListUnavailable(w)
		return
	}
	hold, requestCtx := beginExternalCredentialHold(ctx, gate, a.externalCredentialHeaderBudget())
	document, err := cpa.NewTransportForEndpoint(install.BaseURL).ExternalReadConfigDocument(requestCtx, install.ManagementKey)
	hold.finish()
	if err != nil {
		switch {
		case errors.Is(err, cpa.ErrManagementClientForbidden):
			a.noteExternalRejection(context.WithoutCancel(ctx), grant, err)
			writeExternalManagementForbidden(w)
		case errors.Is(err, cpa.ErrManagementRejected):
			a.noteExternalRejection(context.WithoutCancel(ctx), grant, err)
			writeExternalManagementRejected(w)
		default:
			writeConsoleModelListUnavailable(w)
		}
		return
	}
	clientKey := firstCPAClientKey(document)
	if clientKey == "" {
		writeConsoleModelListEmpty(w)
		return
	}
	proxysvc.ProxyModelListWithClientKey(w, r, install.BaseURL, clientKey, response.Error, response.MethodNotAllowed)
}

// consoleCPADestination resolves the CPA this installation is connected to
// together with the management credential that reads it. Only the committed
// installation authority may name that CPA: a Manager-owned install answers with
// the destination it staged, an External install with the connection the user
// supplied, and an environment-managed connection is the last remaining
// authority.
//
// The committed strategy names that CPA, so the strategy decides the source and
// never the mere presence of one: an installation whose committed strategy is
// External reads the connection the user supplied even while a managed source
// exists in the same process, and a Manager-owned install reads the destination
// it staged. Only an installation with no committed strategy at all falls back
// to whatever source this process holds. A checkpoint that cannot be read fails
// closed: the answer is "this console cannot name the CPA", never a guess.
func (a *freshApplication) consoleCPADestination(ctx context.Context) (string, string, error) {
	if a == nil {
		return "", "", errConsoleModelListUnavailable
	}
	if a.store != nil {
		op, err := a.current(ctx)
		if err != nil {
			return "", "", errConsoleModelListUnavailable
		}
		if op.Strategy.Strategy != "" {
			if op.Strategy.Stage != "committed" {
				return "", "", errConsoleModelListUnavailable
			}
			switch {
			case op.Strategy.Strategy == bootstrap.ConnectExternal:
				if a.externalSource == nil {
					return "", "", errConsoleModelListUnavailable
				}
				install, err := a.externalSource(ctx)
				if err != nil || install.BaseURL == "" || install.ManagementKey == "" {
					return "", "", errConsoleModelListUnavailable
				}
				return install.BaseURL, install.ManagementKey, nil
			case freshkeyapply.ManagerOwnedStrategy(op.Strategy):
				if a.managedSource == nil {
					return "", "", errConsoleModelListUnavailable
				}
				install, err := a.managedSource(ctx)
				if err != nil || install.BaseURL == "" || install.ManagementKey == "" {
					return "", "", errConsoleModelListUnavailable
				}
				return install.BaseURL, install.ManagementKey, nil
			default:
				return "", "", errConsoleModelListUnavailable
			}
		}
	}
	if a.managedSource != nil {
		install, err := a.managedSource(ctx)
		if err != nil || install.BaseURL == "" || install.ManagementKey == "" {
			return "", "", errConsoleModelListUnavailable
		}
		return install.BaseURL, install.ManagementKey, nil
	}
	if a.externalSource != nil {
		install, err := a.externalSource(ctx)
		if err != nil || install.BaseURL == "" || install.ManagementKey == "" {
			return "", "", errConsoleModelListUnavailable
		}
		return install.BaseURL, install.ManagementKey, nil
	}
	if a.cfg.ManagementKey == "" {
		return "", "", errConsoleModelListUnavailable
	}
	baseURL, err := cpa.NormalizeV8BaseURL(a.cfg.CPAUpstreamURL)
	if err != nil {
		return "", "", errConsoleModelListUnavailable
	}
	return baseURL, a.cfg.ManagementKey, nil
}

// consoleCPAClientKey reads one client key the CPA already holds. The read is
// the CPA management projection, authenticated by the saved management
// credential, and it stays in this process: only the list that key authorizes
// leaves. The document carries provider secrets as well, so only the gateway
// client list is projected out of it.
func consoleCPAClientKey(ctx context.Context, baseURL, managementKey string) (string, error) {
	document, err := cpa.NewTransportForEndpoint(baseURL).ReadConfigDocument(ctx, managementKey)
	if err != nil {
		return "", err
	}
	return firstCPAClientKey(document), nil
}

// firstCPAClientKey projects `access.api-keys` out of a CPA configuration
// document. An entry that cannot be sent as a header value is skipped: the
// alternative is a request the transport refuses after the key has already left
// the document.
func firstCPAClientKey(document map[string]any) string {
	access, ok := document["access"].(map[string]any)
	if !ok {
		return ""
	}
	keys, ok := access["api-keys"].([]any)
	if !ok {
		return ""
	}
	for _, entry := range keys {
		key, ok := entry.(string)
		if !ok || strings.TrimSpace(key) == "" || !printableHeaderValue(key) {
			continue
		}
		return key
	}
	return ""
}

// printableHeaderValue reports whether a value can be carried in a header
// without a control character, which is the one shape a request writer refuses.
func printableHeaderValue(value string) bool {
	return !strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// writeConsoleModelListEmpty answers the console with a list a CPA without
// client keys can serve: no models, no error, and no credential presented
// anywhere on the caller's behalf.
func writeConsoleModelListEmpty(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	writeFreshJSON(w, struct {
		Object string `json:"object"`
		Data   []any  `json:"data"`
	}{Object: "list", Data: []any{}})
}

// writeConsoleModelListUnavailable answers a read this installation cannot make.
// It is a 503 with a stable code rather than a 401: the console session was
// verified before this route ran, so its credential is not what failed.
func writeConsoleModelListUnavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	writeFreshJSON(w, struct {
		Code string `json:"code"`
	}{consoleModelListUnavailable})
}
