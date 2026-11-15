package ingress

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
)

const (
	FreshRoutingControlPath   = "/internal/routing/fresh"
	FreshRoutingAuthorityPath = "/internal/routing/fresh-authority"
)

// FreshRoutes is an ephemeral consumer of the Manager's one durable Fresh
// checkpoint. It is not another routing ledger or configuration authority.
type FreshRoutes struct {
	mu    sync.RWMutex
	state FreshRouteState
}
type FreshRouteState struct {
	InstallationID     string `json:"installationId"`
	Generation         string `json:"generation"`
	OperationID        string `json:"operationId"`
	Revision           uint64 `json:"revision"`
	Target             string `json:"target"`
	InferenceTarget    string `json:"inferenceTarget,omitempty"`
	SharedEntry        bool   `json:"sharedEntry,omitempty"`
	Committed          bool   `json:"committed"`
	InferenceCommitted bool   `json:"inferenceCommitted,omitempty"`
}

func (f *FreshRoutes) State() FreshRouteState { f.mu.RLock(); defer f.mu.RUnlock(); return f.state }
func (f *FreshRoutes) Matches(p string) bool {
	s := f.State()
	if s.Target == "" || (p != s.Target && !strings.HasPrefix(p, s.Target+"/")) {
		return false
	}
	if s.SharedEntry && s.InferenceTarget == s.Target {
		subpath := strings.TrimPrefix(p, s.Target)
		if subpath == "" {
			return true
		}
		if IsSupportedInferenceSubpath(subpath) || isBlockedNonInferenceSubpath(subpath) {
			return false
		}
	}
	return true
}
func (f *FreshRoutes) MatchesInferenceCandidate(r *http.Request) bool {
	s := f.State()
	if s.InferenceTarget == "" || r == nil || r.URL == nil {
		return false
	}
	pathValue := r.URL.Path
	if pathValue == "" {
		pathValue = "/"
	}
	for _, raw := range [4]string{pathValue, r.URL.RawPath, r.URL.EscapedPath(), r.RequestURI} {
		if raw == "" {
			continue
		}
		if idx := strings.IndexByte(raw, '?'); idx >= 0 {
			raw = raw[:idx]
		}
		for _, candidate := range routeCandidates(raw) {
			if candidate == s.InferenceTarget || strings.HasPrefix(candidate, s.InferenceTarget+"/") {
				if s.SharedEntry && s.InferenceTarget == s.Target {
					sub := strings.TrimPrefix(candidate, s.InferenceTarget)
					if sub == "" || (!IsSupportedInferenceSubpath(sub) && !isBlockedNonInferenceSubpath(sub)) {
						continue
					}
				}
				return true
			}
		}
	}
	return false
}
func (f *FreshRoutes) Active() bool { return f.State().OperationID != "" }

func (s FreshRouteState) Valid() bool { return validFreshState(s) }

var freshTarget = regexp.MustCompile(`^/[A-Za-z0-9._~$&+,/:;=@-]+$`)

var reservedIngressPrefixes = []string{
	FixedBootstrapDiscoveryPath,
	"/.well-known",
	"/favicon.ico",
	"/apple-touch-icon.png",
	"/assets",
	"/health",
	"/healthz",
	"/keepalive",
	"/management.html",
	"/models",
	"/setup",
	"/status",
	"/v0",
	"/v8",
	"/v1",
	"/v1beta",
	"/openai/v1",
	"/backend-api/codex",
	"/api/provider",
	"/api/v2",
	"/api/gateway",
	"/callback",
	"/anthropic/callback",
	"/codex/callback",
	"/antigravity/callback",
	"/devin/callback",
	"/usage-service",
	"/internal",
	FreshRoutingControlPath,
	FreshRoutingAuthorityPath,
}

func validDynamicPrefix(target string) bool {
	if !freshTarget.MatchString(target) || target == "/" || path.Clean(target) != target || strings.Contains(target, "//") {
		return false
	}
	for _, reserved := range reservedIngressPrefixes {
		if target == reserved || strings.HasPrefix(target, reserved+"/") || strings.HasPrefix(reserved, target+"/") {
			return false
		}
	}
	return true
}

func validFreshState(s FreshRouteState) bool {
	if s.InstallationID == "" || s.Generation == "" || s.OperationID == "" || s.Revision == 0 || !validDynamicPrefix(s.Target) {
		return false
	}
	if s.InferenceTarget != "" {
		if !validDynamicPrefix(s.InferenceTarget) {
			return false
		}
		if s.Target == s.InferenceTarget {
			if !s.SharedEntry {
				return false
			}
		} else {
			if s.SharedEntry {
				return false
			}
			if strings.HasPrefix(s.Target, s.InferenceTarget+"/") || strings.HasPrefix(s.InferenceTarget, s.Target+"/") {
				return false
			}
		}
	} else if s.SharedEntry || s.InferenceCommitted {
		return false
	}
	if s.InferenceCommitted && (!s.Committed || s.InferenceTarget == "") {
		return false
	}
	return true
}

// IsSupportedInferenceSubpath reports whether a prefix-stripped subpath belongs
// to the pinned CPA v8.0.55 public inference protocol families while excluding
// CPA management (/v0, /v8), runtime internal, health, and OAuth callback paths.
func IsSupportedInferenceSubpath(subpath string) bool {
	if subpath == "" || !strings.HasPrefix(subpath, "/") || strings.Contains(subpath, "//") || strings.Contains(subpath, "\\") {
		return false
	}
	if isBlockedNonInferenceSubpath(subpath) {
		return false
	}
	switch {
	case strings.HasPrefix(subpath, "/v1/"):
		if subpath == "/v1/" || subpath == "/v1/runtime" || strings.HasPrefix(subpath, "/v1/runtime/") {
			return false
		}
		return true
	case strings.HasPrefix(subpath, "/v1beta/"):
		return subpath != "/v1beta/"
	case strings.HasPrefix(subpath, "/openai/v1/"):
		return subpath != "/openai/v1/"
	case strings.HasPrefix(subpath, "/backend-api/codex/"):
		return subpath != "/backend-api/codex/"
	case strings.HasPrefix(subpath, "/api/provider/"):
		return subpath != "/api/provider/" && !disallowedProviderSubpath(subpath)
	default:
		return false
	}
}

func isBlockedNonInferenceSubpath(subpath string) bool {
	for _, blocked := range []string{
		"/v0",
		"/v8",
		"/management.html",
		"/health",
		"/healthz",
		"/keepalive",
		"/callback",
		"/anthropic/callback",
		"/codex/callback",
		"/antigravity/callback",
		"/devin/callback",
		"/.well-known",
		"/internal",
		"/usage-service",
	} {
		if subpath == blocked || strings.HasPrefix(subpath, blocked+"/") {
			return true
		}
	}
	return false
}

func disallowedProviderSubpath(subpath string) bool {
	for _, seg := range strings.Split(subpath, "/") {
		if seg == "v0" || seg == "v8" || seg == "management" || seg == "internal" || seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

// StripInferenceRequest validates canonical prefix ownership and strips prefix
// from URL.Path, URL.RawPath, and RequestURI without altering query parameters,
// valid segment escapes, streaming request bodies, or WebSocket upgrade headers.
func StripInferenceRequest(r *http.Request, prefix string) (*http.Request, bool) {
	if r == nil || r.URL == nil || prefix == "" || !validDynamicPrefix(prefix) {
		return nil, false
	}
	if !strings.HasPrefix(r.URL.Path, prefix+"/") {
		return nil, false
	}
	escaped := r.URL.EscapedPath()
	if !strings.HasPrefix(escaped, prefix+"/") {
		return nil, false
	}
	if r.RequestURI != "" {
		rawTarget, _, _ := strings.Cut(r.RequestURI, "?")
		if !strings.HasPrefix(rawTarget, prefix+"/") {
			return nil, false
		}
	}
	strippedPath := strings.TrimPrefix(r.URL.Path, prefix)
	strippedEscaped := strings.TrimPrefix(escaped, prefix)
	if strippedPath == "" || !strings.HasPrefix(strippedPath, "/") || path.Clean(strippedPath) != strippedPath ||
		strings.Contains(strippedPath, "//") || strings.Contains(strippedPath, "\\") ||
		strings.Contains(strippedEscaped, "//") || strings.Contains(strippedEscaped, "\\") {
		return nil, false
	}
	current := strippedEscaped
	for pass := 0; pass < 3; pass++ {
		if path.Clean(current) != current || strings.Contains(current, "//") || strings.Contains(current, "\\") || !IsSupportedInferenceSubpath(current) {
			return nil, false
		}
		if pass == 2 {
			break
		}
		decoded, err := url.PathUnescape(current)
		if err != nil || decoded == current {
			break
		}
		current = decoded
	}
	if !IsSupportedInferenceSubpath(strippedPath) {
		return nil, false
	}
	out := new(http.Request)
	*out = *r
	u := new(url.URL)
	*u = *r.URL
	u.Path = strippedPath
	if r.URL.RawPath != "" {
		u.RawPath = strippedEscaped
	} else {
		u.RawPath = ""
	}
	out.URL = u
	if r.RequestURI != "" {
		if u.RawQuery != "" {
			out.RequestURI = strippedEscaped + "?" + u.RawQuery
		} else {
			out.RequestURI = strippedEscaped
		}
	}
	return out, true
}

// ControlHandler is mounted only on a separate private listener. It holds no
// credential: a mutation is accepted only when the requested non-secret
// projection exactly matches the Manager's current durable Fresh authority.
func (f *FreshRoutes) ControlHandler(authority func(context.Context) (FreshRouteState, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path != FreshRoutingControlPath || r.URL.EscapedPath() != r.URL.Path {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
		case http.MethodPut:
			var requested FreshRouteState
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&requested) != nil || !validFreshState(requested) {
				http.Error(w, "invalid routing projection", http.StatusBadRequest)
				return
			}
			var trailing any
			if decoder.Decode(&trailing) != io.EOF {
				http.Error(w, "invalid routing projection", http.StatusBadRequest)
				return
			}
			if authority == nil {
				http.Error(w, "routing authority unavailable", http.StatusServiceUnavailable)
				return
			}
			owned, err := authority(r.Context())
			if err != nil {
				http.Error(w, "routing authority unavailable", http.StatusServiceUnavailable)
				return
			}
			if owned != requested {
				http.Error(w, "routing authority mismatch", http.StatusConflict)
				return
			}
			f.mu.Lock()
			current := f.state
			conflict := freshTransitionConflict(current, requested)
			if !conflict {
				f.state = requested
			}
			f.mu.Unlock()
			if conflict {
				http.Error(w, "foreign routing owner", http.StatusConflict)
				return
			}
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.State())
	})
}

func freshTransitionConflict(current, requested FreshRouteState) bool {
	if current.OperationID == "" {
		return false
	}
	if current.OperationID != requested.OperationID ||
		current.InstallationID != requested.InstallationID ||
		current.Generation != requested.Generation ||
		current.Target != requested.Target {
		return true
	}
	if current.Committed && !requested.Committed {
		return true
	}
	if current.InferenceCommitted && !requested.InferenceCommitted {
		return true
	}
	// Allow one-time missing-inference completion on an already-committed
	// control route (current.InferenceTarget == "" -> requested.InferenceTarget != ""),
	// while keeping any already-declared InferenceTarget and revision strictly immutable.
	if current.InferenceTarget != "" {
		if requested.InferenceTarget != current.InferenceTarget ||
			requested.SharedEntry != current.SharedEntry ||
			requested.Revision != current.Revision {
			return true
		}
		return false
	}
	if requested.InferenceTarget != "" && current.Committed {
		return requested.Revision < current.Revision
	}
	return requested.Revision != current.Revision
}
