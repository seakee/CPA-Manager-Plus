package main

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
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

// legacyBridgeReachesCPA reports whether a legacy request ends up presenting the
// saved management credential to the user's CPA. Only those requests are gated:
// the Manager-local families answer from its own store, and a page, a static asset
// or a redirect must stay reachable so the user can still read the blocker.
func legacyBridgeReachesCPA(path string) bool {
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
		if app.externalLegacyRefusal(r.Context(), w, r) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// externalLegacyRefusal answers the refusal one legacy request must get on an
// External install, and reports whether it answered.
func (a *freshApplication) externalLegacyRefusal(ctx context.Context, w http.ResponseWriter, r *http.Request) bool {
	if a == nil || a.store == nil || r == nil || r.URL == nil {
		return false
	}
	record, err := a.store.Load(ctx)
	if err != nil || record.Operation.Strategy.Strategy != bootstrap.ConnectExternal {
		// New Managed and Adopt keep the bridge they have always had: on those
		// profiles it is the Console's own contract, not a duplicate route.
		return false
	}
	if externalLegacyReplacedWrite(r.URL.Path, r.Method) {
		writeLegacyRefusal(w, externalLegacyWriteReplaced)
		return true
	}
	if !legacyBridgeReachesCPA(r.URL.Path) || !a.externalReadGrant(ctx).Refused {
		return false
	}
	// The refused credential is the rebind blocker for every method: a read the
	// CPA would refuse again, and a write whose effect cannot be read back.
	writeExternalManagementRejected(w)
	return true
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
