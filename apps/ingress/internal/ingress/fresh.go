package ingress

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
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
	InstallationID string `json:"installationId"`
	Generation     string `json:"generation"`
	OperationID    string `json:"operationId"`
	Revision       uint64 `json:"revision"`
	Target         string `json:"target"`
	Committed      bool   `json:"committed"`
}

func (f *FreshRoutes) State() FreshRouteState { f.mu.RLock(); defer f.mu.RUnlock(); return f.state }
func (f *FreshRoutes) Matches(p string) bool {
	s := f.State()
	return s.Target != "" && (p == s.Target || strings.HasPrefix(p, s.Target+"/"))
}
func (f *FreshRoutes) Active() bool { return f.State().OperationID != "" }

var freshTarget = regexp.MustCompile(`^/[A-Za-z0-9._~$&+,/:;=@-]+$`)

func validFreshState(s FreshRouteState) bool {
	if s.InstallationID == "" || s.Generation == "" || s.OperationID == "" || s.Revision == 0 || !freshTarget.MatchString(s.Target) || s.Target == "/" || path.Clean(s.Target) != s.Target || strings.Contains(s.Target, "//") {
		return false
	}
	for _, reserved := range []string{FixedBootstrapDiscoveryPath, "/favicon.ico", "/apple-touch-icon.png", "/health", "/management.html", "/models", "/setup", "/status", "/v0", "/v1", "/usage-service", FreshRoutingControlPath, FreshRoutingAuthorityPath} {
		if s.Target == reserved || strings.HasPrefix(s.Target, reserved+"/") || strings.HasPrefix(reserved, s.Target+"/") {
			return false
		}
	}
	return true
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
			conflict := current.OperationID != "" && (current.OperationID != requested.OperationID || current.InstallationID != requested.InstallationID || current.Generation != requested.Generation || current.Revision != requested.Revision || current.Target != requested.Target || current.Committed && !requested.Committed)
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
