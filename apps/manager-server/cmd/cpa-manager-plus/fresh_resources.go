package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	proxysvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/proxy"
)

func guardFreshLegacyWrites(next http.Handler, wiringErr error) http.Handler {
	if wiringErr == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Console writes are unavailable", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// externalLegacyWriteReplaced is the stable code of a legacy write that an
// External install already owns through its own route. The legacy bridge is a
// compatibility tunnel; on External the same write exists behind an explicit
// API-key, gateway-config, rebind or usage confirmation contract, so forwarding
// it would be a second, unproven way to change the user's CPA.
const externalLegacyWriteReplaced = "external_legacy_write_replaced"

// legacyWriteReplaced is the stable code of a legacy client-key write refused
// on Managed, Adopt, and Upgrade Managed profiles where client keys are owned
// by the canonical /api/v2/api-keys workspace.
const legacyWriteReplaced = "legacy_write_replaced"

type cpampBridgeExternalConfigWriteContextKey struct{}

func withCPAMPBridgeExternalConfigWrite(ctx context.Context) context.Context {
	return context.WithValue(ctx, cpampBridgeExternalConfigWriteContextKey{}, true)
}

func isCPAMPBridgeExternalConfigWrite(r *http.Request) bool {
	if r == nil {
		return false
	}
	flagged, _ := r.Context().Value(cpampBridgeExternalConfigWriteContextKey{}).(bool)
	return flagged
}

// cpampLegacyClientKeyWriteReplaced reports whether a Full Web `cpamp/` bridge
// request attempts to write client keys through legacy CPA routes on a
// non-External profile.
func cpampLegacyClientKeyWriteReplaced(path, method string) bool {
	if method == http.MethodGet || method == http.MethodHead {
		return false
	}
	trimmed := strings.TrimRight(path, "/")
	switch trimmed {
	case "/v0/management/api-keys", "/v0/management/api-key-aliases", "/v0/management/config.yaml", "/v0/management/config":
		return true
	default:
		return strings.HasPrefix(trimmed, "/v0/management/api-key-aliases/")
	}
}

// writeLegacyRefusal answers one legacy-bridge refusal locally. Nothing is
// forwarded, so a refused code costs the user's CPA no request.
func writeLegacyRefusal(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	writeFreshJSON(w, struct {
		Code string `json:"code"`
	}{code})
}

// externalLegacyReplacedWrite reports whether this legacy request is one of the
// writes Fresh already owns on External. The argument is the path the legacy
// router sees, not the browser-facing `cpamp/` alias.
func externalLegacyReplacedWrite(path, method string) bool {
	if method == http.MethodGet || method == http.MethodHead {
		return false
	}
	trimmed := strings.TrimRight(path, "/")
	switch trimmed {
	case "/v0/management/api-keys", "/v0/management/api-key-aliases", "/v0/management/config.yaml":
		return true
	case "/usage-service/config":
		// Whole-document PUT rewrites the CPA connection and collector settings
		// owned by the committed connection, the rebind and `external/usage`,
		// so editing it here would desync the two. The narrow `PATCH`
		// (`{"enabled": bool}`) owns only Manager's local request-monitoring
		// switch and never touches the External CPA.
		return method != http.MethodPatch
	default:
		return strings.HasPrefix(trimmed, "/v0/management/api-key-aliases/")
	}
}

// externalLegacyLogConfigWrite reports whether this legacy request mutates one
// of the CPA log configuration settings through the compatibility bridge. On
// External installations those writes require proving the CPA v8 version
// contract before forwarding.
func externalLegacyLogConfigWrite(path, method string) bool {
	if method != http.MethodPut {
		return false
	}
	switch strings.TrimRight(path, "/") {
	case "/v0/management/logging-to-file",
		"/v0/management/debug",
		"/v0/management/request-log",
		"/v0/management/logs-max-total-size-mb",
		"/v0/management/error-logs-max-files":
		return true
	default:
		return false
	}
}

var managerLocalManagementFamilySlugs = []string{
	"usage",
	"dashboard",
	"model-prices",
	"monitoring",
	"api-key-aliases",
	"account-action-candidates",
	"codex-inspection",
	"quota-snapshots",
	"cpa-connection",
}

// legacyBridgePresentsSavedKey reports whether a legacy request ends up presenting
// the saved management credential to the user's CPA once its caller is authorized.
// Only those requests are gated: the Manager-local families answer from its own
// store, and a page, a static asset or a redirect must stay reachable so the user
// can still read the blocker. The model list is deliberately absent: it forwards
// the caller's own header, so it is not a presentation of the saved key.
func legacyBridgePresentsSavedKey(r *http.Request) bool {
	path := r.URL.Path
	// The one CPA v8 subtree Manager forwards is the same kind of request: an
	// admin-authorized GET proxied with the saved management key, so the External
	// cutover serializes it and files its refusal exactly like a v0 management
	// read. Only the shape the router will actually proxy counts: every other
	// method or path shape is answered locally with 405/404, so gating it would
	// report a credential failure for a request that never presents the key.
	if proxysvc.IsCPAV8APIKeysConfigPath(path) {
		return r.Method == http.MethodGet && proxysvc.IsCanonicalV8APIKeysConfigRequest(r)
	}
	if proxysvc.IsCPAV8ModelsRefreshPath(path) {
		return r.Method == http.MethodPost && proxysvc.IsCanonicalV8ModelsRefreshRequest(r)
	}
	if proxysvc.IsCPAV8CredentialsPath(path) {
		return r.Method == http.MethodGet && proxysvc.IsCanonicalV8CredentialsRequest(r)
	}
	if proxysvc.IsCPAV8OAuthPath(path) {
		return r.Method == proxysvc.CPAV8OAuthAllowedMethod(path) && proxysvc.IsCanonicalV8OAuthRequest(r)
	}
	cleaned := strings.TrimRight(path, "/")
	if cleaned == "/v0/resource/plugins" || strings.HasPrefix(cleaned, "/v0/resource/plugins/") {
		return true
	}
	if !strings.HasPrefix(cleaned, "/v0/management/") {
		return false
	}
	// Manager-local controllers are dispatched before the CPA proxy in the legacy
	// router, so they never carry the saved key upstream.
	for _, slug := range managerLocalManagementFamilySlugs {
		family := "/v0/management/" + slug
		if cleaned == family || strings.HasPrefix(cleaned, family+"/") {
			return false
		}
	}
	return true
}

// legacyBridgeServicePresentsSavedKey reports whether a Manager-local legacy
// controller route internally presents the saved management credential to the
// user's CPA (synchronously or on a detached async job). Unlike reverse-proxied
// routes, these handlers run a precheck and carry withExternalSavedCredentialGate
// on r.Context() so each individual outbound CPA HTTP call serializes at the wire
// boundary without holding legacyCredentialGate across non-CPA catalog fetches,
// retry backoffs, run leases, or local SQLite transactions.
func legacyBridgeServicePresentsSavedKey(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	cleaned := strings.TrimRight(r.URL.Path, "/")
	switch {
	case r.Method == http.MethodPost && cleaned == "/v0/management/model-prices/sync":
		return true
	case r.Method == http.MethodPost && strings.HasPrefix(cleaned, "/v0/management/account-action-candidates/") && strings.HasSuffix(cleaned, "/enable"):
		candidateID := strings.TrimSuffix(strings.TrimPrefix(cleaned, "/v0/management/account-action-candidates/"), "/enable")
		return candidateID != "" && !strings.Contains(candidateID, "/")
	case r.Method == http.MethodDelete && strings.HasPrefix(cleaned, "/v0/management/account-action-candidates/") && strings.HasSuffix(cleaned, "/auth-file"):
		candidateID := strings.TrimSuffix(strings.TrimPrefix(cleaned, "/v0/management/account-action-candidates/"), "/auth-file")
		return candidateID != "" && !strings.Contains(candidateID, "/")
	case r.Method == http.MethodPost && cleaned == "/v0/management/codex-inspection/run":
		return true
	case r.Method == http.MethodPost && strings.HasPrefix(cleaned, "/v0/management/codex-inspection/runs/") && strings.HasSuffix(cleaned, "/actions"):
		runID := strings.TrimSuffix(strings.TrimPrefix(cleaned, "/v0/management/codex-inspection/runs/"), "/actions")
		return runID != "" && !strings.Contains(runID, "/")
	default:
		return false
	}
}

// externalLegacyDecision is the one answer the External cutover gives a legacy
// request before anything is forwarded.
type externalLegacyDecision int

const (
	// legacyDecisionPassthrough is a request the cutover does not own: another
	// profile's bridge, a Manager-local family, a page, or a request that carries
	// the caller's own credential instead of the saved key.
	legacyDecisionPassthrough externalLegacyDecision = iota
	// legacyDecisionReplacedWrite is a write Fresh already owns on External.
	legacyDecisionReplacedWrite
	// legacyDecisionUnavailable is a saved-key request this Manager cannot decide
	// because its own committed instruction is unreadable.
	legacyDecisionUnavailable
	// legacyDecisionGated is an admin-authorized reverse-proxied request that will
	// present the saved management key, so it is serialized and its refusal recorded.
	legacyDecisionGated
	// legacyDecisionGatedService is an admin-authorized Manager-local controller
	// action that internally presents the saved management key via CPA HTTP helpers.
	legacyDecisionGatedService
)

// externalLegacyDecision decides one legacy request before anything is forwarded,
// and returns the record it decided from, so the gated path does not read the same
// row twice.
func (a *freshApplication) externalLegacyDecision(r *http.Request) (externalLegacyDecision, freshstore.Record) {
	if a == nil || a.store == nil || r == nil || r.URL == nil {
		return legacyDecisionPassthrough, freshstore.Record{}
	}
	presents := legacyBridgePresentsSavedKey(r)
	servicePresents := !presents && legacyBridgeServicePresentsSavedKey(r)
	record, err := a.store.Load(r.Context())
	if err != nil {
		// Fail closed: without the committed instruction this Manager cannot prove
		// which credential a saved-key request would present, and presenting a
		// credential it cannot name is how an address gets banned.
		if presents || servicePresents {
			return legacyDecisionUnavailable, freshstore.Record{}
		}
		return legacyDecisionPassthrough, freshstore.Record{}
	}
	if !isCommittedExternalStrategy(record.Operation.Strategy) {
		// New Managed and Adopt keep the bridge they have always had: on those
		// profiles it is the Console's own contract, not a duplicate route.
		return legacyDecisionPassthrough, record
	}
	if externalLegacyReplacedWrite(r.URL.Path, r.Method) {
		return legacyDecisionReplacedWrite, record
	}
	if !presents && !servicePresents {
		return legacyDecisionPassthrough, record
	}
	// Admin authorization runs before the gate. A caller-auth plugin-management
	// request keeps the caller's own header, and every other unauthorized
	// management request is answered by this Manager itself, so neither presents
	// the saved key: neither may serialize a CPA read or file a refusal.
	if !freshAdminAuthenticated(record, r) {
		return legacyDecisionPassthrough, record
	}
	if servicePresents {
		return legacyDecisionGatedService, record
	}
	return legacyDecisionGated, record
}

// freshAdminAuthenticated reports whether the request carries this installation's
// Admin key. The gate sits above the legacy router, where the same key is verified
// again per controller, so the gate has to decide before it takes the credential
// lock.
func freshAdminAuthenticated(record freshstore.Record, r *http.Request) bool {
	if record.Admin == nil || r == nil {
		return false
	}
	return security.VerifyAdminKey(*record.Admin, security.ExtractBearerToken(r.Header.Get("Authorization")))
}

// guardFreshExternalLegacy is the External cutover of the legacy bridge. The
// bridge is a compatibility tunnel onto the same routes Fresh now owns behind an
// explicit contract, so on External it must not become a second, unproven write
// path onto the user's CPA; and it must never present a management credential
// that CPA already refused. Both answers are local, so neither costs the user's
// CPA a request. New Managed and Adopt hand the request straight through.
func guardFreshExternalLegacy(next http.Handler, app *freshApplication) http.Handler {
	if next == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decision, _ := app.externalLegacyDecision(r)
		switch decision {
		case legacyDecisionReplacedWrite:
			writeLegacyRefusal(w, externalLegacyWriteReplaced)
			return
		case legacyDecisionUnavailable:
			writeExternalAuthorityUnavailable(w)
			return
		case legacyDecisionGatedService:
			ctx := r.Context()
			gate := &app.legacyCredentialGate
			if !gate.acquire(ctx) {
				writeExternalCredentialWaitCancelled(w)
				return
			}
			record, err := app.store.Load(ctx)
			if err != nil {
				gate.release()
				if ctx.Err() != nil {
					writeExternalCredentialWaitCancelled(w)
					return
				}
				writeExternalAuthorityUnavailable(w)
				return
			}
			if !isCommittedExternalStrategy(record.Operation.Strategy) || !freshAdminAuthenticated(record, r) {
				gate.release()
				next.ServeHTTP(w, r)
				return
			}
			var grant externalReadGrant
			snapshotCtx := withFreshRecordSnapshot(ctx, record)
			if app.externalSource != nil {
				resolvedGrant, _, targetErr := app.externalReadTargetFor(snapshotCtx, record)
				if targetErr != nil {
					gate.release()
					if ctx.Err() != nil {
						writeExternalCredentialWaitCancelled(w)
						return
					}
					if errors.Is(targetErr, cpa.ErrExternalTargetMoved) {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusConflict)
						writeFreshJSON(w, struct {
							Code string `json:"code"`
						}{freshconfig.ReasonTargetMoved})
						return
					}
					writeExternalAuthorityUnavailable(w)
					return
				}
				grant = resolvedGrant
			} else {
				resolvedGrant, grantErr := app.externalReadGrantFor(snapshotCtx, record)
				if grantErr != nil {
					gate.release()
					if ctx.Err() != nil {
						writeExternalCredentialWaitCancelled(w)
						return
					}
					writeExternalAuthorityUnavailable(w)
					return
				}
				grant = resolvedGrant
			}
			if grant.Refused {
				gate.release()
				writeExternalManagementRejected(w)
				return
			}
			gate.release()
			next.ServeHTTP(w, r.WithContext(app.withExternalSavedCredentialGate(ctx)))
			return
		case legacyDecisionGated:
		default:
			next.ServeHTTP(w, r)
			return
		}
		if app != nil && app.legacyApp != nil && app.legacyApp.ProxyService != nil {
			nextReq, releaseAuthFile, status, authErr := app.legacyApp.ProxyService.AcquireRequestAuthFileMutation(r)
			if authErr != nil {
				response.Error(w, status, authErr)
				return
			}
			defer releaseAuthFile()
			r = nextReq
		}
		ctx := r.Context()
		gate := &app.legacyCredentialGate
		// A waiter whose request was cancelled gives up instead of queueing: no CPA
		// request is ever made on behalf of a caller that stopped waiting. It is told
		// so locally rather than left with a connection that never answers.
		if !gate.acquire(ctx) {
			writeExternalCredentialWaitCancelled(w)
			return
		}
		// Re-load the Fresh record inside legacyCredentialGate so both the
		// refusal check/grant and the credential forwarded by the downstream
		// proxy come from one post-wait snapshot even across a concurrent rebind.
		record, err := app.store.Load(ctx)
		if err != nil {
			gate.release()
			writeExternalAuthorityUnavailable(w)
			return
		}
		if !isCommittedExternalStrategy(record.Operation.Strategy) || !freshAdminAuthenticated(record, r) {
			gate.release()
			next.ServeHTTP(w, r)
			return
		}
		var (
			grant   externalReadGrant
			install freshkeyapply.ExternalInstall
		)
		snapshotCtx := withFreshRecordSnapshot(ctx, record)
		snapshotCtx = app.withExternalSavedCredentialGate(snapshotCtx)
		if app.externalSource != nil {
			resolvedGrant, resolvedInstall, targetErr := app.externalReadTargetFor(snapshotCtx, record)
			if targetErr != nil {
				gate.release()
				if ctx.Err() != nil {
					writeExternalCredentialWaitCancelled(w)
					return
				}
				if errors.Is(targetErr, cpa.ErrExternalTargetMoved) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusConflict)
					writeFreshJSON(w, struct {
						Code string `json:"code"`
					}{freshconfig.ReasonTargetMoved})
					return
				}
				writeExternalAuthorityUnavailable(w)
				return
			}
			grant = resolvedGrant
			install = resolvedInstall
			snapshotCtx = proxysvc.WithSavedSetupOverride(snapshotCtx, install.BaseURL, install.ManagementKey)
		} else {
			resolvedGrant, grantErr := app.externalReadGrantFor(snapshotCtx, record)
			if grantErr != nil {
				gate.release()
				if ctx.Err() != nil {
					writeExternalCredentialWaitCancelled(w)
					return
				}
				writeExternalAuthorityUnavailable(w)
				return
			}
			grant = resolvedGrant
		}
		if grant.Refused {
			gate.release()
			writeExternalManagementRejected(w)
			return
		}
		if isCPAMPBridgeExternalConfigWrite(r) {
			gate.release()
			writeLegacyRefusal(w, externalLegacyWriteReplaced)
			return
		}
		markContext, mark := proxysvc.WithSavedKeyMark(snapshotCtx)
		// A console read's tenure is bounded in both directions: the recorder frees
		// the gate the moment the status exists, and the budget frees it and ends
		// the request when the CPA never sends one. Without the budget a CPA that
		// accepts the connection and never answers would hold every other console
		// read. A request that waits on an upstream provider takes no budget at all
		// and keeps the tenure until its status arrives, exactly as the proxy
		// itself bounds it.
		hold, requestCtx := app.beginCredentialHold(markContext, app.externalCredentialHoldBudget(r.URL.Path))
		if externalLegacyLogConfigWrite(r.URL.Path, r.Method) {
			if app.externalSource == nil {
				hold.finish()
				writeExternalAuthorityUnavailable(w)
				return
			}
			report, probeErr := cpa.ObserveExternalVersion(cpa.WithoutExternalHeaderRelease(requestCtx), install.BaseURL, install.ManagementKey)
			if probeErr != nil {
				app.noteExternalRejection(context.WithoutCancel(ctx), grant, probeErr)
				hold.finish()
				switch {
				case errors.Is(probeErr, cpa.ErrManagementClientForbidden):
					writeExternalManagementForbidden(w)
				case errors.Is(probeErr, cpa.ErrManagementRejected):
					writeExternalManagementRejected(w)
				case errors.Is(probeErr, cpa.ErrExternalTargetMoved):
					writeLegacyRefusal(w, freshconfig.ReasonTargetMoved)
				case errors.Is(probeErr, errExternalCredentialWaitCancelled) || ctx.Err() != nil:
					writeExternalCredentialWaitCancelled(w)
				default:
					writeExternalAuthorityUnavailable(w)
				}
				return
			}
			if report.WriteBlocked {
				hold.finish()
				writeLegacyRefusal(w, freshconfig.ReasonVersionUnsupported)
				return
			}
		}
		recorder := &externalLegacyRecorder{
			ResponseWriter: w,
			// The refusal is recorded against a context the caller cannot cancel: a
			// browser that disconnects while the CPA answers 401 must still leave the
			// gate in place, or the next page load presents the refused key again.
			ctx: context.WithoutCancel(requestCtx), app: app, hold: hold, grant: grant, mark: mark,
		}
		// The lock is held only until the status is known, which the recorder does;
		// this defer covers the paths where no status ever reaches it.
		defer hold.finish()
		next.ServeHTTP(recorder, r.WithContext(requestCtx))
	})
}

// externalCredentialGate serializes the legacy requests that present the saved
// management credential. It is a channel rather than a mutex so a waiter can give
// up when its own request is cancelled: holding a console page behind a caller that
// is already gone would stall every other CPA read for no answer.
type externalCredentialGate struct {
	once   sync.Once
	slot   chan struct{}
	mu     sync.Mutex
	onWait func()
}

func (g *externalCredentialGate) setOnWait(fn func()) {
	g.mu.Lock()
	g.onWait = fn
	g.mu.Unlock()
}

func (g *externalCredentialGate) acquire(ctx context.Context) bool {
	g.once.Do(func() { g.slot = make(chan struct{}, 1) })
	select {
	case g.slot <- struct{}{}:
		return true
	default:
	}
	g.mu.Lock()
	onWait := g.onWait
	g.mu.Unlock()
	if onWait != nil {
		onWait()
	}
	select {
	case g.slot <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// release returns the gate. It is safe to call more than once: the recorder
// releases on the status and its caller releases on every other exit.
func (g *externalCredentialGate) release() {
	g.once.Do(func() { g.slot = make(chan struct{}, 1) })
	select {
	case <-g.slot:
	default:
	}
}

// externalCredentialHeaderBudgetDefault is how long one gated console read may hold
// the credential gate waiting for the CPA's status. It is deliberately longer than
// any management read the console makes and shorter than a browser's own patience:
// its only job is to keep a CPA that never answers from stalling every other read.
// It is never applied to a request that waits on an upstream provider — see
// externalLegacyWaitsOnProvider.
const externalCredentialHeaderBudgetDefault = 30 * time.Second

// externalCredentialHeaderBudget reports the bounded tenure of the credential gate
// for one request.
func (a *freshApplication) externalCredentialHeaderBudget() time.Duration {
	if a != nil && a.credentialHeaderBudget > 0 {
		return a.credentialHeaderBudget
	}
	return externalCredentialHeaderBudgetDefault
}

// externalCredentialHoldBudget is the tenure bound of one gated legacy request. A
// request that waits on an upstream provider gets none: its status is decided by
// that provider's own latency, not by how long a console read should take.
func (a *freshApplication) externalCredentialHoldBudget(path string) time.Duration {
	if externalLegacyWaitsOnProvider(path) {
		return 0
	}
	return a.externalCredentialHeaderBudget()
}

// externalLegacyWaitsOnProvider reports whether a gated legacy request's status is
// the upstream provider's, not the CPA's management handlers'. The proxy puts no
// response-header timeout on these paths — it uses the default transport, so the
// caller's own deadline is the only bound — and the gate must not add one: a
// non-streaming completion that takes a minute to send its headers is a healthy
// answer, not a hung CPA, and cutting it turns it into a 502.
//
// `api-call` forwards an arbitrary provider request. `oauth-callback` and CPA
// v8 `/v8/management/oauth/callback` exchange an authorization code for tokens
// at the provider, and that exchange is the provider's latency too.
func externalLegacyWaitsOnProvider(path string) bool {
	switch strings.TrimRight(path, "/") {
	case "/v0/management/api-call", "/v0/management/oauth-callback", "/v8/management/oauth/callback":
		return true
	default:
		return false
	}
}

// statusClientClosedRequest names a request whose caller stopped waiting before
// any answer existed. It is the Client Closed Request status (499) proxies already
// use for exactly this fact.
const statusClientClosedRequest = 499

// externalCredentialWaitCancelled is the local code for a gated request that gave
// up on the credential lock because its own caller was gone. No CPA request was
// made, so it is not a refusal of the stored credential.
const externalCredentialWaitCancelled = "external_credential_wait_cancelled"

// writeExternalCredentialWaitCancelled answers a cancelled waiter locally instead
// of returning silently: an abandoned connection that never receives a status is
// indistinguishable from a Manager that hung.
func writeExternalCredentialWaitCancelled(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusClientClosedRequest)
	writeFreshJSON(w, struct {
		Code string `json:"code"`
	}{externalCredentialWaitCancelled})
}

// externalCredentialHold is one request's tenure of the credential gate. The gate
// is released the moment the response status is known, and, on a console read, the
// budget releases it and ends the request when the CPA never produces one, so a
// hung CPA cannot hold every other console read behind it. A provider-bound request
// takes no budget: its own status or its caller's cancellation ends the tenure.
type externalCredentialHoldState uint8

const (
	holdStateWaitingHeaders externalCredentialHoldState = iota
	holdStateClaimed
	holdStateReleased
)

type externalCredentialHold struct {
	mu          sync.Mutex
	state       externalCredentialHoldState
	gate        *externalCredentialGate
	timer       *time.Timer
	cancel      context.CancelFunc
	afterExpiry func()
}

type externalCredentialHoldContextKey struct{}

func externalCredentialHoldFromContext(ctx context.Context, gate *externalCredentialGate) *externalCredentialHold {
	if ctx == nil || gate == nil {
		return nil
	}
	held, _ := ctx.Value(externalCredentialHoldContextKey{}).(*externalCredentialHold)
	if held == nil || held.gate != gate {
		return nil
	}
	return held
}

func hasExternalCredentialHold(ctx context.Context, gate *externalCredentialGate) bool {
	return externalCredentialHoldFromContext(ctx, gate) != nil
}

// beginCredentialHold takes the tenure of an already-acquired gate for app.
func (a *freshApplication) beginCredentialHold(
	ctx context.Context,
	budget time.Duration,
) (*externalCredentialHold, context.Context) {
	return beginExternalCredentialHold(ctx, &a.legacyCredentialGate, budget, a.afterCredentialHoldBudgetExpiry)
}

// beginExternalCredentialHold takes the tenure of an already-acquired gate and
// returns the context the proxied request must use: cancelling it is what ends a
// request whose CPA never answered. A non-positive budget means the request carries
// no header budget of its own, and only its status or its caller ends the tenure.
func beginExternalCredentialHold(
	ctx context.Context,
	gate *externalCredentialGate,
	budget time.Duration,
	onBudgetExpiry ...func(),
) (*externalCredentialHold, context.Context) {
	var afterExpiry func()
	if len(onBudgetExpiry) > 0 {
		afterExpiry = onBudgetExpiry[0]
	}
	hold := &externalCredentialHold{
		state:       holdStateWaitingHeaders,
		gate:        gate,
		afterExpiry: afterExpiry,
	}
	requestCtx, cancel := context.WithCancel(context.WithValue(ctx, externalCredentialHoldContextKey{}, hold))
	hold.cancel = cancel
	if budget > 0 {
		hold.mu.Lock()
		hold.timer = time.AfterFunc(budget, hold.expireBudget)
		hold.mu.Unlock()
	}
	return hold, requestCtx
}

// claimHeader transitions the hold from waiting-for-headers to claimed so a
// concurrent tenure-budget expiry cannot release the gate before status
// classification and durable refusal persistence complete.
func (h *externalCredentialHold) claimHeader() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state == holdStateReleased {
		return false
	}
	h.state = holdStateClaimed
	return true
}

func (h *externalCredentialHold) expireBudget() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.state != holdStateWaitingHeaders {
		afterExpiry := h.afterExpiry
		h.mu.Unlock()
		if afterExpiry != nil {
			afterExpiry()
		}
		return
	}
	h.state = holdStateReleased
	gate := h.gate
	cancel := h.cancel
	afterExpiry := h.afterExpiry
	h.mu.Unlock()
	if gate != nil {
		gate.release()
	}
	if cancel != nil {
		cancel()
	}
	if afterExpiry != nil {
		afterExpiry()
	}
}

// release frees the gate and stops the budget. It is safe to call more than once:
// the recorder releases on the status, on a hijack and on every other exit.
func (h *externalCredentialHold) release() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.state == holdStateReleased {
		h.mu.Unlock()
		return
	}
	h.state = holdStateReleased
	timer := h.timer
	gate := h.gate
	h.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if gate != nil {
		gate.release()
	}
}

// finish ends the tenure once ServeHTTP returned. The request context is cancelled
// only here: a streamed body whose status was already known keeps its own context.
func (h *externalCredentialHold) finish() {
	if h == nil {
		return
	}
	h.release()
	if h.cancel != nil {
		h.cancel()
	}
}

// externalLegacyRecorder turns a CPA credential rejection into the typed blocker
// while passing every other response through untouched. The status is decided
// before any byte is written, so a streamed or large body is never buffered, and
// the credential gate is released right there: serializing the body past the moment
// the CPA authenticated would let one slow reader stall every other console read.
type externalLegacyRecorder struct {
	http.ResponseWriter
	ctx   context.Context
	app   *freshApplication
	hold  *externalCredentialHold
	grant externalReadGrant
	mark  *proxysvc.SavedKeyMark

	decided bool
	refused bool
}

func (w *externalLegacyRecorder) release() { w.hold.release() }

func (w *externalLegacyRecorder) WriteHeader(status int) {
	if w.decided {
		return
	}
	if status < http.StatusOK {
		// An informational status is not the answer. A 100 or a 103 says nothing
		// about the credential, and deciding on it would release the gate before the
		// CPA had a chance to refuse: the final status is what decides both.
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.decided = true
	if w.hold != nil {
		w.hold.claimHeader()
	}
	// Only a refusal of the saved key is this gate's business. The caller-auth
	// entry points forward the caller's own header, and a 401 from one of those
	// says nothing about the stored key: recording it would block a healthy
	// installation until someone rebound.
	if w.mark.Attached() {
		switch status {
		case http.StatusUnauthorized:
			w.refused = true
			w.app.noteExternalRejection(w.ctx, w.grant, cpa.ErrManagementRejected)
			writeExternalManagementRejected(w.ResponseWriter)
			w.release()
			return
		case http.StatusForbidden:
			// CPA refused this client rather than the credential: a ban, or remote
			// management disabled for it. It is the same blocker the 2a routes name,
			// with the detail that says the ban expires.
			w.refused = true
			w.app.noteExternalRejection(w.ctx, w.grant, cpa.ErrManagementClientForbidden)
			writeExternalManagementForbidden(w.ResponseWriter)
			w.release()
			return
		}
	}
	w.ResponseWriter.WriteHeader(status)
	w.release()
}

// Hijack releases the gate before handing the connection over. An upgraded
// connection never carries a status this gate can decide, so the lock must not
// outlive the handshake; the caller still owns the lifetime of the connection.
func (w *externalLegacyRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	w.release()
	w.decided = true
	return hijacker.Hijack()
}

// Unwrap lets http.ResponseController reach the real writer, so streaming a
// legacy response through this gate still flushes.
func (w *externalLegacyRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *externalLegacyRecorder) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		if !w.decided {
			w.WriteHeader(http.StatusOK)
		}
		flusher.Flush()
	}
}

func (w *externalLegacyRecorder) Write(body []byte) (int, error) {
	if w.refused {
		// The CPA's body described a refusal this Manager has already replaced.
		return len(body), nil
	}
	if !w.decided {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

// writeExternalAuthorityUnavailable fails a saved-key request closed: this Manager
// could not read its own committed instruction, so it cannot prove which credential
// the request would present, and it forwards nothing.
func writeExternalAuthorityUnavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	writeFreshJSON(w, struct {
		Code string `json:"code"`
	}{"external_cpa_authority_unavailable"})
}

// legacyResource exposes the existing CPAMP service/controller contracts below
// the canonical Full Web API namespace. The browser never learns or constructs
// the legacy paths; this adapter keeps the Public API authority and admin
// verification at the Fresh boundary while reusing the original business
// services and response shapes.
func (a *freshApplication) legacyResource(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation, resource string) {
	if a.legacy == nil {
		http.Error(w, "resource unavailable", http.StatusServiceUnavailable)
		return
	}
	// Full Web uses this bridge only after canonicalAPI has verified the
	// Manager Admin key. Keep the browser-facing namespace explicit while
	// reusing the complete legacy router and its own controller checks. The
	// External cutover runs inside a.legacy itself, on the rewritten path, so
	// every entry point into that router is covered by one gate.
	bridged := strings.TrimPrefix(resource, "cpamp/")
	if strings.HasPrefix(resource, "cpamp/") && legacyBridgePathAllowed(bridged, r.Method) {
		// The model list is the one bridged read a CPA answers with its own
		// gateway vocabulary. The legacy route forwards the caller's own header,
		// and the console's header is the Admin credential a CPA does not accept
		// as a gateway key, so that read is answered server-side with the client
		// key the CPA already holds instead of being forwarded.
		if consoleModelListResource(bridged, r.Method) {
			a.consoleModelList(w, r)
			return
		}
		parsed, err := url.Parse("/" + bridged)
		if err != nil || parsed.Path == "" || parsed.Path == "/" {
			http.NotFound(w, r)
			return
		}
		if !isCommittedExternalStrategy(op.Strategy) && cpampLegacyClientKeyWriteReplaced(parsed.Path, r.Method) {
			writeLegacyRefusal(w, legacyWriteReplaced)
			return
		}
		parsed.RawQuery = r.URL.RawQuery
		clone := r.Clone(r.Context())
		clone.URL = parsed
		clone.RequestURI = parsed.RequestURI()
		if isCommittedExternalStrategy(op.Strategy) &&
			strings.TrimRight(parsed.Path, "/") == "/v0/management/config" &&
			r.Method != http.MethodGet && r.Method != http.MethodHead {
			clone = clone.WithContext(withCPAMPBridgeExternalConfigWrite(clone.Context()))
		}
		if sanitizer := bridgedReadSanitizer(parsed.Path, r.Method); sanitizer != nil {
			clone.Header = clone.Header.Clone()
			stripBridgeSanitizedRequestHeaders(clone.Header)
			sanitizingWriter := newBridgeSanitizingWriter(w, sanitizer)
			a.serveLegacyResource(sanitizingWriter, r, clone, op, bridged)
			sanitizingWriter.finish(r.Method)
			return
		}
		a.serveLegacyResource(w, r, clone, op, bridged)
		return
	}
	var target string
	switch {
	case resource == "cpamp/dashboard" && r.Method == http.MethodGet:
		// The legacy dashboard contract requires a day boundary. Keep this
		// server-owned so normal Console navigation has no internal query step.
		today := time.Now()
		start := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location()).UnixMilli()
		target = "/v0/management/dashboard/summary?today_start_ms=" + strconv.FormatInt(start, 10)
	case resource == "cpamp/config" && (r.Method == http.MethodGet || r.Method == http.MethodPut || r.Method == http.MethodPatch):
		target = "/usage-service/config"
	case resource == "cpamp/status" && r.Method == http.MethodGet:
		target = "/status"
	case resource == "cpamp/usage" && r.Method == http.MethodGet:
		target = "/v0/management/usage"
	case resource == "cpamp/model-prices" && (r.Method == http.MethodGet || r.Method == http.MethodPut):
		target = "/v0/management/model-prices"
	case resource == "cpamp/monitoring" && r.Method == http.MethodPost:
		target = "/v0/management/monitoring/analytics"
	default:
		http.NotFound(w, r)
		return
	}
	parsed, err := url.Parse(target)
	if err != nil {
		http.Error(w, "resource unavailable", http.StatusServiceUnavailable)
		return
	}
	clone := r.Clone(r.Context())
	clone.URL = parsed
	clone.RequestURI = parsed.RequestURI()
	a.serveLegacyResource(w, r, clone, op, strings.TrimPrefix(parsed.Path, "/"))
}

// serveLegacyResource forwards one bridged request and, when that request wrote
// Manager's usage-service configuration, reconciles the host-bound Collector
// with what was stored. A narrow PATCH flips only collector.enabled and keeps a
// live Collector untouched on On->On; a whole-document PUT reloads the
// Collector whenever its effective RuntimeConfig or connection authority changed.
func (a *freshApplication) serveLegacyResource(
	w http.ResponseWriter,
	r *http.Request,
	clone *http.Request,
	op bootstrap.FreshOperation,
	target string,
) {
	if !legacyMonitoringSwitchWrite(target, r.Method) {
		a.legacy.ServeHTTP(w, clone)
		return
	}
	hasCollectorPatch := false
	hasEnabledPatch := false
	if r.Method == http.MethodPatch && clone.Body != nil {
		rawBody, readErr := io.ReadAll(io.LimitReader(clone.Body, 4096+1))
		if readErr != nil || len(rawBody) > 4096 {
			w.Header().Set("Connection", "close")
			_ = http.NewResponseController(w).SetReadDeadline(time.Unix(1, 0))
			_ = clone.Body.Close()
		} else {
			_ = clone.Body.Close()
		}
		if readErr == nil {
			clone.Body = io.NopCloser(bytes.NewReader(rawBody))
			if len(rawBody) <= 4096 {
				var bodyMap map[string]json.RawMessage
				if json.Unmarshal(rawBody, &bodyMap) == nil {
					for key := range bodyMap {
						if strings.EqualFold(key, "collector") {
							hasCollectorPatch = true
						} else if strings.EqualFold(key, "enabled") {
							hasEnabledPatch = true
						}
					}
				}
			}
		}
	}
	recorder := &legacyStatusRecorder{ResponseWriter: w}
	a.legacy.ServeHTTP(recorder, clone)
	if recorder.status >= http.StatusOK && recorder.status < http.StatusMultipleChoices {
		switch {
		case r.Method == http.MethodPut || hasCollectorPatch:
			a.applyUsageManagerConfig(r.Context(), op)
		case hasEnabledPatch:
			a.applyUsageMonitoringSwitch(r.Context(), op)
		}
	}
}

// legacyMonitoringSwitchWrite reports whether a bridged request writes the
// request-monitoring switch. Both spellings the Console uses end at the same
// legacy endpoint.
func legacyMonitoringSwitchWrite(target, method string) bool {
	if method != http.MethodPut && method != http.MethodPatch {
		return false
	}
	return strings.TrimRight(target, "/") == "usage-service/config"
}

// legacyStatusRecorder keeps the status of one bridged write without changing
// the response it forwards. Only switch writes are recorded, so the rest of the
// bridge still hands the real writer to the legacy router untouched.
type legacyStatusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *legacyStatusRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *legacyStatusRecorder) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(body)
}

func (r *legacyStatusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *legacyStatusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

const maxBridgedSanitizedBodyBytes = 16 << 20

type bridgePayloadSanitizer func([]byte) (sanitized []byte, contentType string, err error)

func bridgedReadSanitizer(path, method string) bridgePayloadSanitizer {
	if method != http.MethodGet && method != http.MethodHead {
		return nil
	}
	switch strings.TrimRight(path, "/") {
	case "/v0/management/api-keys":
		return sanitizeBridgedAPIKeysJSON
	case "/v0/management/config":
		return sanitizeBridgedConfigJSON
	case "/v0/management/config.yaml":
		return sanitizeBridgedConfigYAML
	default:
		return nil
	}
}

func maskBridgedClientKey(raw string) string {
	if masked := model.MaskedClientKey(raw); masked != "" {
		return masked
	}
	return model.MaskedImportedClientKey(raw)
}

func decodeStrictJSONObject(raw []byte) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("expected JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var doc map[string]json.RawMessage
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, errors.New("unexpected trailing JSON data")
	}
	if doc == nil {
		return nil, errors.New("expected non-null JSON object")
	}
	return doc, nil
}

func sanitizeBridgedAPIKeysJSON(raw []byte) ([]byte, string, error) {
	doc, err := decodeStrictJSONObject(raw)
	if err != nil {
		return nil, "", err
	}
	matched := false
	for key, fieldRaw := range doc {
		if !strings.EqualFold(key, "api-keys") && !strings.EqualFold(key, "apikeys") {
			continue
		}
		if key == "api-keys" {
			matched = true
		}
		if bytes.Equal(bytes.TrimSpace(fieldRaw), []byte("null")) {
			doc[key] = json.RawMessage("[]")
			continue
		}
		var keys []string
		if err := json.Unmarshal(fieldRaw, &keys); err != nil {
			return nil, "", err
		}
		masked := make([]string, len(keys))
		for i, secret := range keys {
			masked[i] = maskBridgedClientKey(secret)
		}
		encoded, err := json.Marshal(masked)
		if err != nil {
			return nil, "", err
		}
		doc[key] = encoded
	}
	if !matched {
		doc["api-keys"] = json.RawMessage("[]")
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, "", err
	}
	return encoded, "application/json", nil
}

func validateBridgedClientKeyArrayOrNull(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var keys []string
	return json.Unmarshal(trimmed, &keys)
}

func sanitizeBridgedConfigJSON(raw []byte) ([]byte, string, error) {
	doc, err := decodeStrictJSONObject(raw)
	if err != nil {
		return nil, "", err
	}
	hasAccessObject := false
	for key, accessRaw := range doc {
		if !strings.EqualFold(key, "access") {
			continue
		}
		trimmed := bytes.TrimSpace(accessRaw)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			continue
		}
		accessMap, err := decodeStrictJSONObject(trimmed)
		if err != nil {
			return nil, "", err
		}
		hasAccessObject = true
		for subKey, subRaw := range accessMap {
			if strings.EqualFold(subKey, "api-keys") || strings.EqualFold(subKey, "apikeys") {
				if err := validateBridgedClientKeyArrayOrNull(subRaw); err != nil {
					return nil, "", err
				}
				accessMap[subKey] = json.RawMessage("[]")
			}
		}
		encodedAccess, err := json.Marshal(accessMap)
		if err != nil {
			return nil, "", err
		}
		doc[key] = encodedAccess
	}
	matched := false
	for key, valRaw := range doc {
		if strings.EqualFold(key, "api-keys") || strings.EqualFold(key, "apikeys") {
			trimmed := bytes.TrimSpace(valRaw)
			if len(trimmed) > 0 && trimmed[0] == '{' {
				// Preserve CPA v8 top-level provider-group map intact.
				if _, err := decodeStrictJSONObject(trimmed); err != nil {
					return nil, "", err
				}
				if key == "api-keys" {
					matched = true
				}
				continue
			}
			if err := validateBridgedClientKeyArrayOrNull(valRaw); err != nil {
				return nil, "", err
			}
			doc[key] = json.RawMessage("[]")
			if key == "api-keys" {
				matched = true
			}
		}
	}
	if !matched && !hasAccessObject {
		doc["api-keys"] = json.RawMessage("[]")
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, "", err
	}
	return encoded, "application/json", nil
}

func stripBridgeSanitizedRequestHeaders(h http.Header) {
	if h == nil {
		return
	}
	h.Del("Accept-Encoding")
	h.Del("Range")
	h.Del("If-Range")
	h.Del("If-None-Match")
	h.Del("If-Modified-Since")
}

const maxYAMLNodesForBridgeSanitize = 100000

func countYAMLNodesBounded(node *yaml.Node, budget int) (int, bool) {
	if node == nil {
		return 0, true
	}
	if budget <= 0 {
		return 0, false
	}
	total := 1
	for _, child := range node.Content {
		sub, ok := countYAMLNodesBounded(child, budget-total)
		if !ok {
			return 0, false
		}
		total += sub
	}
	return total, true
}

func yamlTreeHasAnchorsOrAliases(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return true
	}
	for _, child := range node.Content {
		if yamlTreeHasAnchorsOrAliases(child) {
			return true
		}
	}
	return false
}

func collectStringSliceElements(val any, dst map[string]struct{}) bool {
	if val == nil {
		return true
	}
	items, ok := val.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return false
		}
		if trimmed := strings.TrimSpace(s); trimmed != "" {
			dst[s] = struct{}{}
			dst[trimmed] = struct{}{}
		}
	}
	return true
}
func scrubResolvedYAMLSecrets(val any, secrets map[string]struct{}) any {
	if len(secrets) == 0 || val == nil {
		return val
	}
	switch typed := val.(type) {
	case string:
		if _, hit := secrets[typed]; hit {
			return ""
		}
		if _, hit := secrets[strings.TrimSpace(typed)]; hit {
			return ""
		}
		return typed
	case []any:
		kept := make([]any, 0, len(typed))
		for _, elem := range typed {
			if s, ok := elem.(string); ok {
				if _, hit := secrets[s]; hit {
					continue
				}
				if _, hit := secrets[strings.TrimSpace(s)]; hit {
					continue
				}
			}
			kept = append(kept, scrubResolvedYAMLSecrets(elem, secrets))
		}
		return kept
	case map[string]any:
		for k, v := range typed {
			typed[k] = scrubResolvedYAMLSecrets(v, secrets)
		}
		return typed
	default:
		return val
	}
}

func sanitizeBridgedConfigYAML(raw []byte) ([]byte, string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return []byte("{}\n"), "application/yaml; charset=utf-8", nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(trimmed))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, "", err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, "", errors.New("unexpected multi-document config.yaml")
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, "", errors.New("unexpected config.yaml document")
	}
	if _, ok := countYAMLNodesBounded(&doc, maxYAMLNodesForBridgeSanitize); !ok {
		return nil, "", errors.New("config.yaml exceeds maximum YAML node budget")
	}
	root := doc.Content[0]
	if root.Kind == yaml.ScalarNode && (root.Tag == "!!null" || root.Value == "") {
		return []byte("{}\n"), "application/yaml; charset=utf-8", nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, "", errors.New("expected mapping root in config.yaml")
	}

	// Decode into map[string]any so YAML anchors (*alias) and merge keys (<<)
	// are fully resolved before stripping client API keys, and comments are
	// dropped unconditionally.
	var resolved map[string]any
	if err := yaml.Unmarshal(trimmed, &resolved); err != nil {
		return nil, "", err
	}
	if resolved == nil {
		return []byte("{}\n"), "application/yaml; charset=utf-8", nil
	}

	secrets := map[string]struct{}{}
	for key, val := range resolved {
		switch {
		case strings.EqualFold(key, "api-keys") || strings.EqualFold(key, "apikeys"):
			// In v0/v6 config.yaml, top-level api-keys is a sequence of client keys;
			// in v8 config.yaml, top-level api-keys may be a provider-group map.
			if _, isProviderMap := val.(map[string]any); !isProviderMap {
				if !collectStringSliceElements(val, secrets) {
					return nil, "", errors.New("expected sequence or mapping api-keys in config.yaml")
				}
				resolved[key] = []any{}
			}
		case strings.EqualFold(key, "access"):
			if val == nil {
				continue
			}
			accessMap, ok := val.(map[string]any)
			if !ok {
				return nil, "", errors.New("expected mapping access node in config.yaml")
			}
			for subKey, subVal := range accessMap {
				if strings.EqualFold(subKey, "api-keys") || strings.EqualFold(subKey, "apikeys") {
					if !collectStringSliceElements(subVal, secrets) {
						return nil, "", errors.New("expected sequence access.api-keys in config.yaml")
					}
					accessMap[subKey] = []any{}
				}
			}
		}
	}
	if len(secrets) > 0 && yamlTreeHasAnchorsOrAliases(&doc) {
		scrubResolvedYAMLSecrets(resolved, secrets)
	}

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(resolved); err != nil {
		return nil, "", err
	}
	if err := enc.Close(); err != nil {
		return nil, "", err
	}
	return out.Bytes(), "application/yaml; charset=utf-8", nil
}

type bridgeSanitizingResponseWriter struct {
	dst       http.ResponseWriter
	sanitizer bridgePayloadSanitizer
	header    http.Header
	status    int
	overflow  bool
	buf       bytes.Buffer
}

func newBridgeSanitizingWriter(dst http.ResponseWriter, sanitizer bridgePayloadSanitizer) *bridgeSanitizingResponseWriter {
	return &bridgeSanitizingResponseWriter{
		dst:       dst,
		sanitizer: sanitizer,
		header:    make(http.Header),
	}
}

func copyHeadersPreservingNoStore(dst, src http.Header) {
	keepNoStore := dst.Get("Cache-Control") == "no-store"
	for k, vals := range src {
		dst.Del(k)
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
	if keepNoStore {
		dst.Set("Cache-Control", "no-store")
	}
}

func isAllowedBridgedErrorCode(code string) bool {
	switch code {
	case freshconfig.ReasonManagementRejected,
		freshconfig.ReasonTargetMoved,
		freshconfig.ReasonUnavailable,
		freshconfig.ReasonVersionUnsupported,
		"external_credential_wait_cancelled",
		"client_key_management_authority_required":
		return true
	default:
		return false
	}
}

func isAllowedBridgedErrorDetail(detail string) bool {
	switch detail {
	case "",
		"client_forbidden",
		externalRebindForbiddenDetail,
		externalRebindRejectedDetail:
		return true
	default:
		return false
	}
}

func isAllowedBridgedErrorMessage(msg string) bool {
	switch strings.ToLower(strings.TrimSpace(msg)) {
	case "unauthorized",
		"forbidden",
		"not found",
		"method not allowed",
		"bad request",
		"conflict",
		"service unavailable",
		"upstream management error":
		return true
	default:
		return false
	}
}

func safeBridgedErrorPayload(raw []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, true
	}
	if len(trimmed) > 1024 || trimmed[0] != '{' {
		return nil, false
	}
	doc, err := decodeStrictJSONObject(trimmed)
	if err != nil {
		return nil, false
	}
	for k, v := range doc {
		var str string
		if json.Unmarshal(v, &str) != nil || len(str) > 256 {
			return nil, false
		}
		switch k {
		case "code":
			if !isAllowedBridgedErrorCode(str) {
				return nil, false
			}
		case "detail":
			if !isAllowedBridgedErrorDetail(str) {
				return nil, false
			}
		case "error":
			if !isAllowedBridgedErrorMessage(str) {
				return nil, false
			}
		default:
			return nil, false
		}
	}
	return trimmed, true
}

func (w *bridgeSanitizingResponseWriter) Header() http.Header {
	return w.header
}

func (w *bridgeSanitizingResponseWriter) WriteHeader(code int) {
	if code < http.StatusOK {
		copyHeadersPreservingNoStore(w.dst.Header(), w.header)
		w.header = make(http.Header)
		w.dst.WriteHeader(code)
		return
	}
	if w.status != 0 {
		return
	}
	w.status = code
}

func (w *bridgeSanitizingResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.overflow {
		return len(p), nil
	}
	if w.buf.Len()+len(p) > maxBridgedSanitizedBodyBytes {
		w.overflow = true
		w.buf.Reset()
		return len(p), nil
	}
	return w.buf.Write(p)
}

func (w *bridgeSanitizingResponseWriter) Flush() {}

func (w *bridgeSanitizingResponseWriter) Unwrap() http.ResponseWriter {
	return w.dst
}

func (w *bridgeSanitizingResponseWriter) finish(method string) {
	if strings.TrimSpace(w.header.Get("Content-Encoding")) != "" {
		response.Error(w.dst, http.StatusBadGateway, errors.New("unexpected encoded upstream management response"))
		return
	}
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	if status != http.StatusOK {
		if status < http.StatusBadRequest {
			response.Error(w.dst, http.StatusBadGateway, errors.New("unexpected upstream management response status"))
			return
		}
		copyHeadersPreservingNoStore(w.dst.Header(), w.header)
		w.dst.Header().Del("Content-Length")
		w.dst.Header().Del("Content-Encoding")
		if !w.overflow {
			if safeBody, ok := safeBridgedErrorPayload(w.buf.Bytes()); ok {
				if len(safeBody) > 0 && w.dst.Header().Get("Content-Type") == "" {
					w.dst.Header().Set("Content-Type", "application/json")
				}
				w.dst.WriteHeader(status)
				if method != http.MethodHead && len(safeBody) > 0 {
					_, _ = w.dst.Write(safeBody)
				}
				return
			}
		}
		w.dst.Header().Set("Content-Type", "application/json")
		w.dst.WriteHeader(status)
		if method != http.MethodHead {
			_, _ = w.dst.Write([]byte(`{"error":"upstream management error"}`))
		}
		return
	}
	if method == http.MethodHead && w.buf.Len() == 0 && !w.overflow {
		copyHeadersPreservingNoStore(w.dst.Header(), w.header)
		w.dst.Header().Del("Content-Length")
		w.dst.Header().Del("Content-Encoding")
		w.dst.WriteHeader(http.StatusOK)
		return
	}
	if w.overflow {
		response.Error(w.dst, http.StatusBadGateway, errors.New("upstream management response too large"))
		return
	}
	sanitized, fallbackContentType, err := w.sanitizer(w.buf.Bytes())
	if err != nil {
		response.Error(w.dst, http.StatusBadGateway, errors.New("invalid upstream management response"))
		return
	}
	copyHeadersPreservingNoStore(w.dst.Header(), w.header)
	w.dst.Header().Del("Content-Length")
	w.dst.Header().Del("Content-Encoding")
	if fallbackContentType == "application/json" || w.dst.Header().Get("Content-Type") == "" {
		w.dst.Header().Set("Content-Type", fallbackContentType)
	}
	w.dst.WriteHeader(http.StatusOK)
	if method != http.MethodHead {
		_, _ = w.dst.Write(sanitized)
	}
}

// legacyBridgePathAllowed is an endpoint-and-method allowlist. The
// canonical API has already authenticated the request, but a generic path
// tunnel would turn the Full Web namespace into an unbounded Manager router.
// These families are the existing CPAMP contracts consumed by MainLayout and
// its real pages; unknown paths remain a fail-closed 404.
func legacyBridgePathAllowed(resource, method string) bool {
	if resource == "" || strings.ContainsAny(resource, "\\#?%") || strings.Contains(resource, "..") {
		return false
	}
	allowed := func(methods ...string) bool {
		return slices.Contains(methods, method)
	}
	switch resource {
	case "usage-service/info":
		return allowed(http.MethodGet)
	case "usage-service/config":
		return allowed(http.MethodGet, http.MethodPut, http.MethodPatch)
	case "usage-service/account-processing-policy":
		return allowed(http.MethodGet, http.MethodPatch)
	case "usage-service/quota-cooldowns":
		return allowed(http.MethodGet)
	case "usage-service/quota-cooldowns/recover":
		return allowed(http.MethodPost)
	case "usage-service/updates":
		return allowed(http.MethodGet)
	case "usage-service/updates/check", "usage-service/updates/notification", "usage-service/updates/dismiss":
		return allowed(http.MethodPost)
	case "usage-service/updates/channel":
		return allowed(http.MethodPut)
	case "usage-service/runtime/updates":
		return allowed(http.MethodGet)
	case "usage-service/runtime/updates/check":
		return allowed(http.MethodPost)
	case "usage-service/runtime/updates/prepare", "usage-service/runtime/updates/prepare-upload", "usage-service/runtime/updates/activate":
		return allowed(http.MethodPost)
	case "usage-service/runtime/updates/operations/prepare", "usage-service/runtime/updates/operations/activate":
		return allowed(http.MethodGet)
	case "v0/management/logs":
		return allowed(http.MethodGet, http.MethodDelete)
	case "v0/management/request-error-logs":
		return allowed(http.MethodGet)
	case "v0/management/codex-auth-url", "v0/management/anthropic-auth-url", "v0/management/antigravity-auth-url", "v0/management/kimi-auth-url", "v0/management/kimi-ai-auth-url", "v0/management/xai-auth-url", "v0/management/devin-auth-url", "v0/management/meta-auth-url", "v0/management/get-auth-status":
		return allowed(http.MethodGet)
	case "v0/management/oauth-callback":
		return allowed(http.MethodPost)
	case "v0/management/oauth-session":
		return allowed(http.MethodDelete)
	case "v0/management/latest-version":
		return allowed(http.MethodGet)
	case "v0/management/config.yaml":
		return allowed(http.MethodGet, http.MethodPut)
	case "v0/management/api-keys":
		return allowed(http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete)
	case "v0/management/api-key-aliases":
		return allowed(http.MethodGet, http.MethodPut)
	case "v0/management/codex-api-key", "v0/management/claude-api-key", "v0/management/gemini-api-key", "v0/management/interactions-api-key", "v0/management/vertex-api-key", "v0/management/xai-api-key", "v0/management/openai-compatibility":
		return allowed(http.MethodGet, http.MethodPut, http.MethodDelete)
	case "v0/management/config":
		return allowed(http.MethodGet, http.MethodPut)
	case "v0/management/debug", "v0/management/request-retry", "v0/management/quota-exceeded/switch-project", "v0/management/quota-exceeded/switch-preview-model", "v0/management/request-log", "v0/management/logging-to-file", "v0/management/ws-auth":
		return allowed(http.MethodPut)
	case "v0/management/proxy-url":
		return allowed(http.MethodPut, http.MethodDelete)
	case "v0/management/logs-max-total-size-mb", "v0/management/error-logs-max-files", "v0/management/force-model-prefix", "v0/management/routing/strategy":
		return allowed(http.MethodGet, http.MethodPut)
	case "v0/management/api-key-usage":
		return allowed(http.MethodGet)
	case "v0/management/api-call", "v0/management/vertex/import":
		return allowed(http.MethodPost)
	case "v0/management/dashboard/summary", "v0/management/usage", "v0/management/usage/export", "v0/management/usage/maintenance", "v0/management/model-prices/usage-summary":
		return allowed(http.MethodGet)
	case "v0/management/usage/archives":
		return allowed(http.MethodGet, http.MethodPost)
	case "v0/management/usage/archives/preview":
		return allowed(http.MethodPost)
	case "v0/management/usage/import":
		return allowed(http.MethodPost)
	case "v0/management/usage/import-sessions":
		return allowed(http.MethodGet, http.MethodPost)
	case "v0/management/model-prices":
		return allowed(http.MethodGet, http.MethodPut)
	case "v0/management/model-prices/sync":
		return allowed(http.MethodPost)
	case "v0/management/account-action-candidates":
		return allowed(http.MethodGet)
	case "v0/management/codex-inspection/runs":
		return allowed(http.MethodGet)
	case "v0/management/monitoring/header-snapshots":
		return allowed(http.MethodGet)
	case "v0/management/monitoring/account-history", "v0/management/monitoring/account-window-usage", "v0/management/monitoring/analytics":
		return allowed(http.MethodPost)
	case "v0/management/quota-snapshots", "v0/management/quota-snapshots/query":
		return allowed(http.MethodPost)
	case "v0/management/auth-files":
		return allowed(http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete)
	case "v0/management/auth-files/status":
		return allowed(http.MethodGet, http.MethodPatch)
	case "v0/management/auth-files/fields":
		return allowed(http.MethodGet, http.MethodPatch)
	case "v0/management/auth-files/download", "v0/management/auth-files/models":
		return allowed(http.MethodGet)
	case "v0/management/oauth-excluded-models":
		return allowed(http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete)
	case "v0/management/oauth-model-alias":
		return allowed(http.MethodGet, http.MethodPatch, http.MethodDelete)
	case "v0/management/reset-quota":
		return allowed(http.MethodPost)
	case "v0/management/plugins", "v0/management/plugin-store":
		return allowed(http.MethodGet)
	case "v8/management/models/refresh":
		return allowed(http.MethodPost)
	case "v8/management/credentials":
		return allowed(http.MethodGet)
	case "v8/management/oauth/siwc/bind":
		return allowed(http.MethodPost)
	case "v8/management/oauth/callback":
		return allowed(http.MethodPost)
	case "v8/management/oauth/status":
		return allowed(http.MethodGet)
	case "v8/management/oauth/session":
		return allowed(http.MethodDelete)
	case "status", "health", "v1/models", "models":
		return allowed(http.MethodGet)
	default:
		return legacyBridgeDynamicPathAllowed(resource, method, allowed)
	}
}

func legacyBridgeDynamicPathAllowed(resource, method string, allowed func(...string) bool) bool {
	const management = "v0/management/"
	if !strings.HasPrefix(resource, management) {
		return false
	}
	path := strings.TrimPrefix(resource, management)
	parts := strings.Split(path, "/")
	if len(parts) == 1 {
		if strings.HasSuffix(parts[0], "-auth-url") &&
			validBridgeOAuthProviderSlug(strings.TrimSuffix(parts[0], "-auth-url")) {
			return allowed(http.MethodGet)
		}
		return false
	}
	if !safeBridgePathSegment(parts[len(parts)-1]) {
		return false
	}
	// Request logs and API-key aliases use opaque single path segments.
	if len(parts) == 2 && parts[0] == "request-error-logs" {
		return allowed(http.MethodGet)
	}
	if len(parts) == 2 && parts[0] == "request-log-by-id" {
		return allowed(http.MethodGet)
	}
	if len(parts) == 2 && parts[0] == "api-key-aliases" {
		return allowed(http.MethodDelete)
	}
	if len(parts) == 2 && parts[0] == "model-definitions" {
		return allowed(http.MethodGet)
	}
	if len(parts) == 2 && parts[0] == "codex-inspection" && parts[1] == "run" {
		return allowed(http.MethodPost)
	}
	if len(parts) == 2 && parts[0] == "usage" && parts[1] == "import-sessions" {
		return allowed(http.MethodGet, http.MethodPost)
	}
	if len(parts) == 3 && parts[0] == "usage" && parts[1] == "archives" {
		return allowed(http.MethodGet)
	}
	if len(parts) == 4 && parts[0] == "usage" && parts[1] == "archives" && safeBridgePathSegment(parts[2]) {
		switch parts[3] {
		case "resume", "verify", "delete", "cancel":
			return allowed(http.MethodPost)
		}
	}
	if len(parts) == 3 && parts[0] == "codex-inspection" && parts[1] == "runs" && safeBridgeNumericSegment(parts[2]) {
		return allowed(http.MethodGet)
	}
	if len(parts) == 4 && parts[0] == "codex-inspection" && parts[1] == "runs" && safeBridgeNumericSegment(parts[2]) {
		switch parts[3] {
		case "cancel", "actions":
			return allowed(http.MethodPost)
		}
	}
	if len(parts) == 3 && parts[0] == "account-action-candidates" && safeBridgeNumericSegment(parts[1]) {
		switch parts[2] {
		case "ignore", "resolve", "enable":
			return allowed(http.MethodPost)
		case "auth-file":
			return allowed(http.MethodDelete)
		}
	}
	if len(parts) == 3 && parts[0] == "usage" && parts[1] == "import-sessions" && safeBridgePathSegment(parts[2]) {
		return allowed(http.MethodGet, http.MethodDelete)
	}
	if len(parts) == 4 && parts[0] == "usage" && parts[1] == "import-sessions" && safeBridgePathSegment(parts[2]) {
		switch parts[3] {
		case "chunk":
			return allowed(http.MethodPut)
		case "complete":
			return allowed(http.MethodPost)
		}
	}
	if len(parts) == 2 && parts[0] == "plugin-store" && safeBridgePathSegment(parts[1]) {
		return allowed(http.MethodPost)
	}
	if len(parts) == 3 && parts[0] == "plugin-store" && safeBridgePathSegment(parts[1]) && parts[2] == "install" {
		return allowed(http.MethodPost)
	}
	if len(parts) == 2 && parts[0] == "plugins" && safeBridgePathSegment(parts[1]) {
		return allowed(http.MethodDelete)
	}
	if len(parts) == 3 && parts[0] == "plugins" && safeBridgePathSegment(parts[1]) {
		switch parts[2] {
		case "enabled":
			return allowed(http.MethodPatch)
		case "config":
			return allowed(http.MethodGet, http.MethodPut, http.MethodPatch)
		case "quota":
			return allowed(http.MethodGet, http.MethodPost, http.MethodDelete)
		}
	}
	if len(parts) == 4 && parts[0] == "plugins" && safeBridgePathSegment(parts[1]) && parts[2] == "quota" && parts[3] == "reset" {
		return allowed(http.MethodPost)
	}
	return false
}

func safeBridgePathSegment(value string) bool {
	if value == "" || strings.ContainsAny(value, "/\\#?%") {
		return false
	}
	for _, b := range []byte(value) {
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_' || b == '.' {
			continue
		}
		return false
	}
	return value != "." && value != ".."
}

func validBridgeOAuthProviderSlug(slug string) bool {
	if slug == "" || isManagerLocalManagementFamilySlug(slug) {
		return false
	}
	prevHyphen := true
	for _, b := range []byte(slug) {
		if b == '-' {
			if prevHyphen {
				return false
			}
			prevHyphen = true
			continue
		}
		if (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') {
			prevHyphen = false
			continue
		}
		return false
	}
	return !prevHyphen
}

func isManagerLocalManagementFamilySlug(slug string) bool {
	for _, reserved := range managerLocalManagementFamilySlugs {
		if slug == reserved || strings.HasPrefix(slug, reserved+"-") {
			return true
		}
	}
	return false
}

func safeBridgeNumericSegment(value string) bool {
	if value == "" {
		return false
	}
	for _, b := range []byte(value) {
		if b < '0' || b > '9' {
			return false
		}
	}
	return true
}
