package main

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
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
	switch strings.TrimRight(path, "/") {
	case "/v0/management/api-keys", "/v0/management/config.yaml", "/usage-service/config":
		// API keys are owned by `api-keys`, the gateway document by
		// `gateway/config`, and the usage-service settings document by the
		// connection, the rebind and `external/usage`. On External the setup row
		// is a mirror of the committed connection, so editing it here would
		// desync the two.
		return true
	}
	return false
}

// legacyBridgePresentsSavedKey reports whether a legacy request ends up presenting
// the saved management credential to the user's CPA once its caller is authorized.
// Only those requests are gated: the Manager-local families answer from its own
// store, and a page, a static asset or a redirect must stay reachable so the user
// can still read the blocker. The model list is deliberately absent: it forwards
// the caller's own header, so it is not a presentation of the saved key.
func legacyBridgePresentsSavedKey(path string) bool {
	cleaned := strings.TrimRight(path, "/")
	if cleaned == "/v0/resource/plugins" || strings.HasPrefix(cleaned, "/v0/resource/plugins/") {
		return true
	}
	if !strings.HasPrefix(cleaned, "/v0/management/") {
		return false
	}
	// Manager-local controllers are dispatched before the CPA proxy in the legacy
	// router, so they never carry the saved key upstream.
	for _, family := range []string{
		"/v0/management/usage",
		"/v0/management/dashboard",
		"/v0/management/model-prices",
		"/v0/management/monitoring",
		"/v0/management/api-key-aliases",
		"/v0/management/account-action-candidates",
		"/v0/management/codex-inspection",
		"/v0/management/quota-snapshots",
		"/v0/management/cpa-connection",
	} {
		if cleaned == family || strings.HasPrefix(cleaned, family+"/") {
			return false
		}
	}
	return true
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
	// legacyDecisionGated is an admin-authorized request that will present the saved
	// management key, so it is serialized and its refusal recorded.
	legacyDecisionGated
)

// externalLegacyDecision decides one legacy request before anything is forwarded,
// and returns the record it decided from, so the gated path does not read the same
// row twice.
func (a *freshApplication) externalLegacyDecision(r *http.Request) (externalLegacyDecision, freshstore.Record) {
	if a == nil || a.store == nil || r == nil || r.URL == nil {
		return legacyDecisionPassthrough, freshstore.Record{}
	}
	presents := legacyBridgePresentsSavedKey(r.URL.Path)
	record, err := a.store.Load(r.Context())
	if err != nil {
		// Fail closed: without the committed instruction this Manager cannot prove
		// which credential a saved-key request would present, and presenting a
		// credential it cannot name is how an address gets banned.
		if presents {
			return legacyDecisionUnavailable, freshstore.Record{}
		}
		return legacyDecisionPassthrough, freshstore.Record{}
	}
	if record.Operation.Strategy.Strategy != bootstrap.ConnectExternal {
		// New Managed and Adopt keep the bridge they have always had: on those
		// profiles it is the Console's own contract, not a duplicate route.
		return legacyDecisionPassthrough, record
	}
	if externalLegacyReplacedWrite(r.URL.Path, r.Method) {
		return legacyDecisionReplacedWrite, record
	}
	if !presents {
		return legacyDecisionPassthrough, record
	}
	// Admin authorization runs before the gate. A caller-auth plugin-management
	// request keeps the caller's own header, and every other unauthorized
	// management request is answered by this Manager itself, so neither presents
	// the saved key: neither may serialize a CPA read or file a refusal.
	if !freshAdminAuthenticated(record, r) {
		return legacyDecisionPassthrough, record
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
		decision, record := app.externalLegacyDecision(r)
		switch decision {
		case legacyDecisionReplacedWrite:
			writeLegacyRefusal(w, externalLegacyWriteReplaced)
			return
		case legacyDecisionUnavailable:
			writeExternalAuthorityUnavailable(w)
			return
		case legacyDecisionGated:
		default:
			next.ServeHTTP(w, r)
			return
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
		// One at a time, and the refusal is re-read under the lock: a page fires
		// several of these in parallel, and the CPA bans an address after five
		// failed authentications. Without this, one page load is enough to trip
		// the ban and the blocker could never be cleared.
		//
		// The grant is taken here, before the request leaves. It is the credential
		// this request is about to send, so a refusal is filed against that and not
		// against whatever a concurrent rebind stored while it was in flight.
		grant := app.externalReadGrantFor(ctx, record)
		if grant.Refused {
			gate.release()
			// The refused credential is the rebind blocker for every method: a read
			// the CPA would refuse again, and a write whose effect cannot be read
			// back.
			writeExternalManagementRejected(w)
			return
		}
		// A stale credential is refused by the CPA with 401, and the browser reads
		// a 401 from this namespace as its own Admin session having expired: it
		// clears the session and returns to the login form before any page can
		// name the cause. The refusal is the same fact the typed reads block on, so
		// it is recorded and answered as that blocker instead.
		markContext, mark := proxysvc.WithSavedKeyMark(ctx)
		// The tenure is bounded in both directions: the recorder frees the gate the
		// moment the status exists, and the budget frees it and ends the request
		// when the CPA never sends one. Without the budget a CPA that accepts the
		// connection and never answers would hold every other console read.
		hold, requestCtx := beginExternalCredentialHold(markContext, gate, app.externalCredentialHeaderBudget())
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
	once sync.Once
	slot chan struct{}
}

func (g *externalCredentialGate) acquire(ctx context.Context) bool {
	g.once.Do(func() { g.slot = make(chan struct{}, 1) })
	select {
	case g.slot <- struct{}{}:
		return true
	default:
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

// externalCredentialHeaderBudgetDefault is how long one gated request may hold the
// credential gate waiting for the CPA's status. It is deliberately longer than any
// management read the console makes and shorter than a browser's own patience: its
// only job is to keep a CPA that never answers from stalling every other read.
const externalCredentialHeaderBudgetDefault = 30 * time.Second

// externalCredentialHeaderBudget reports the bounded tenure of the credential gate
// for one request.
func (a *freshApplication) externalCredentialHeaderBudget() time.Duration {
	if a != nil && a.credentialHeaderBudget > 0 {
		return a.credentialHeaderBudget
	}
	return externalCredentialHeaderBudgetDefault
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
// is released the moment the response status is known, and the budget releases it
// and ends the request when the CPA never produces one, so a hung CPA cannot hold
// every other console read behind it.
type externalCredentialHold struct {
	gate *externalCredentialGate
	// timer is stored atomically because the budget callback can fire while the
	// handle is still being built: the callback must never read a field the
	// constructor has not published yet.
	timer  atomic.Pointer[time.Timer]
	cancel context.CancelFunc
	once   sync.Once
}

// beginExternalCredentialHold takes the bounded tenure of an already-acquired gate
// and returns the context the proxied request must use: cancelling it is what ends
// a request whose CPA never answered.
func beginExternalCredentialHold(
	ctx context.Context,
	gate *externalCredentialGate,
	budget time.Duration,
) (*externalCredentialHold, context.Context) {
	requestCtx, cancel := context.WithCancel(ctx)
	hold := &externalCredentialHold{gate: gate, cancel: cancel}
	hold.timer.Store(time.AfterFunc(budget, func() {
		hold.release()
		cancel()
	}))
	return hold, requestCtx
}

// release frees the gate and stops the budget. It is safe to call more than once:
// the recorder releases on the status, on a hijack and on every other exit.
func (h *externalCredentialHold) release() {
	h.once.Do(func() {
		if timer := h.timer.Load(); timer != nil {
			timer.Stop()
		}
		h.gate.release()
	})
}

// finish ends the tenure once ServeHTTP returned. The request context is cancelled
// only here: a streamed body whose status was already known keeps its own context.
func (h *externalCredentialHold) finish() {
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
func (a *freshApplication) legacyResource(w http.ResponseWriter, r *http.Request, resource string) {
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
		parsed, err := url.Parse("/" + bridged)
		if err != nil || parsed.Path == "" || parsed.Path == "/" {
			http.NotFound(w, r)
			return
		}
		parsed.RawQuery = r.URL.RawQuery
		clone := r.Clone(r.Context())
		clone.URL = parsed
		clone.RequestURI = parsed.RequestURI()
		a.legacy.ServeHTTP(w, clone)
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
	case resource == "cpamp/config" && (r.Method == http.MethodGet || r.Method == http.MethodPut):
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
	a.legacy.ServeHTTP(w, clone)
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
		return allowed(http.MethodGet, http.MethodPut)
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
	case "usage-service/runtime/updates/prepare", "usage-service/runtime/updates/activate":
		return allowed(http.MethodPost)
	case "usage-service/runtime/updates/operations/prepare", "usage-service/runtime/updates/operations/activate":
		return allowed(http.MethodGet)
	case "v0/management/logs":
		return allowed(http.MethodGet, http.MethodDelete)
	case "v0/management/request-error-logs":
		return allowed(http.MethodGet)
	case "v0/management/codex-auth-url", "v0/management/anthropic-auth-url", "v0/management/antigravity-auth-url", "v0/management/kimi-auth-url", "v0/management/xai-auth-url", "v0/management/get-auth-status":
		return allowed(http.MethodGet)
	case "v0/management/oauth-callback":
		return allowed(http.MethodPost)
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
	case "v0/management/logs-max-total-size-mb", "v0/management/force-model-prefix", "v0/management/routing/strategy":
		return allowed(http.MethodGet, http.MethodPut)
	case "v0/management/api-key-usage":
		return allowed(http.MethodGet)
	case "v0/management/api-call", "v0/management/vertex/import":
		return allowed(http.MethodPost)
	case "v0/management/dashboard/summary", "v0/management/usage", "v0/management/usage/export", "v0/management/model-prices/usage-summary":
		return allowed(http.MethodGet)
	case "v0/management/usage/import", "v0/management/usage/import-sessions":
		return allowed(http.MethodPost)
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
	if len(parts) < 2 || !safeBridgePathSegment(parts[len(parts)-1]) {
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
		return allowed(http.MethodPost)
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
