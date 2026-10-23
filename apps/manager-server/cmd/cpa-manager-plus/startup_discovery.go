package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	routingdomain "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/routing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradeauth"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

// startupHTTPHandler owns the fixed discovery path while the normal Manager
// graph is still being constructed. Before ready, every unrelated path is
// rejected locally; after ready, only the existing application handler is
// delegated to. The fixed path remains captured for the lifetime of the
// process, including the future completed terminal behavior.
type startupHTTPHandler struct {
	discovery         routingdomain.DiscoveryHandler
	protected         http.Handler
	readOnlyShell     http.Handler
	projectionMu      sync.RWMutex
	projection        bootstrap.PublicDiscovery
	readOnlyDiscovery http.Handler
	upgradeAuth       http.Handler
	next              atomic.Value // *startupHandlerTarget
	lifecycle         atomic.Value // routingdomain.DiscoveryLifecycle
	ready             atomic.Bool
	fresh             atomic.Pointer[startupHandlerTarget]
	pahS0             bool
}

type startupHandlerTarget struct {
	handler http.Handler
}

func newStartupHTTPHandler(result bootstrap.PublicDiscovery) *startupHTTPHandler {
	h := &startupHTTPHandler{projection: result, readOnlyShell: httpapi.BootstrapWebHandler()}
	h.lifecycle.Store(routingdomain.DiscoveryActive)
	h.discovery = routingdomain.DiscoveryHandler{
		Lifecycle: h.discoveryLifecycle,
		Active:    http.HandlerFunc(h.serveDiscovery),
	}
	h.protected = httpapi.ProtectBrandResources(routingdomain.ProtectFixedDiscovery(http.HandlerFunc(h.serveNonDiscovery), http.HandlerFunc(h.serveFixedBootstrap)))
	h.next.Store(&startupHandlerTarget{handler: http.NotFoundHandler()})
	return h
}

// The blocked/pre-ready operator may inspect the same bounded public discovery
// in a read-only shell. The separate Upgrade leaf authenticates only; no
// Bootstrap API or Upgrade writer is mounted.
func (h *startupHTTPHandler) serveFixedBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == bootstrap.FixedBootstrapDiscoveryPath {
		h.discovery.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == upgradeauth.Path {
		h.projectionMu.RLock()
		defer h.projectionMu.RUnlock()
		if !h.ready.Load() && h.discoveryLifecycle() == routingdomain.DiscoveryActive && h.readOnlyDiscovery != nil && h.upgradeAuth != nil {
			h.upgradeAuth.ServeHTTP(w, r)
		} else {
			http.NotFound(w, r)
		}
		return
	}
	if h.ready.Load() || h.discoveryLifecycle() != routingdomain.DiscoveryActive ||
		(r.Method != http.MethodGet && r.Method != http.MethodHead) ||
		(r.URL.Path != bootstrap.FixedBootstrapUIPath &&
			!strings.HasPrefix(r.URL.Path, bootstrap.FixedBootstrapAssetsPath)) {
		http.NotFound(w, r)
		return
	}
	h.projectionMu.RLock()
	readOnly := h.readOnlyDiscovery != nil
	h.projectionMu.RUnlock()
	if !readOnly || h.readOnlyShell == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	h.readOnlyShell.ServeHTTP(w, r)
}

func (h *startupHTTPHandler) serveDiscovery(w http.ResponseWriter, r *http.Request) {
	h.projectionMu.RLock()
	defer h.projectionMu.RUnlock()
	if h.readOnlyDiscovery != nil {
		h.readOnlyDiscovery.ServeHTTP(w, r)
		return
	}
	bootstrapdiscovery.Handler{Discovery: h.projection}.ServeHTTP(w, r)
}

// EnableReadOnlyDiscovery projects only current, read-only evidence. Fresh or
// partial sources with no database are inventory-only observations. Existing
// databases may use a bounded rollback-mode SQLite read snapshot without
// creating a Manager marker; WAL/journal recovery remains unavailable here.
func (h *startupHTTPHandler) EnableReadOnlyDiscovery(paths sqliterepo.InspectionPaths, options legacypreflight.Options, timeout time.Duration) {
	h.projectionMu.Lock()
	defer h.projectionMu.Unlock()
	// Entropy failure leaves this leaf closed without weakening discovery.
	h.upgradeAuth = nil
	if handler, err := upgradeauth.New(paths, os.Getenv("CPAMP_PUBLIC_ORIGIN")); err == nil {
		h.upgradeAuth = handler
	}
	h.readOnlyDiscovery = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		legacypreflight.WithFencedInspection(ctx, paths, options, func(report legacypreflight.Report, err error) {
			result := bootstrapdiscovery.Project(bootstrapdiscovery.Input{Report: report, InspectionError: err})
			bootstrapdiscovery.Handler{Discovery: result}.ServeHTTP(w, r)
		})
	})
}

// BeginWritableStartup drains pre-write projections before their evidence can
// become stale. It neither completes Bootstrap nor grants a new classification.
func (h *startupHTTPHandler) BeginWritableStartup() {
	h.projectionMu.Lock()
	defer h.projectionMu.Unlock()
	h.projection = bootstrapdiscovery.Unavailable()
	h.readOnlyDiscovery = nil
	h.upgradeAuth = nil
}

func (h *startupHTTPHandler) discoveryLifecycle() routingdomain.DiscoveryLifecycle {
	lifecycle, _ := h.lifecycle.Load().(routingdomain.DiscoveryLifecycle)
	if lifecycle == "" {
		return routingdomain.DiscoveryUnavailable
	}
	return lifecycle
}

func (h *startupHTTPHandler) SetReady(next http.Handler) {
	if next == nil {
		next = http.NotFoundHandler()
	}
	h.next.Store(&startupHandlerTarget{handler: next})
	h.ready.Store(true)
}

func (h *startupHTTPHandler) SetCompleted() {
	h.lifecycle.Store(routingdomain.DiscoveryCompleted)
}

func (h *startupHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.servePAHS0(w, r) {
		return
	}
	if target := h.fresh.Load(); target != nil {
		target.handler.ServeHTTP(w, r)
		return
	}
	h.protected.ServeHTTP(w, r)
}

func (h *startupHTTPHandler) SetFreshHandler(handler http.Handler) {
	if handler != nil {
		h.fresh.Store(&startupHandlerTarget{handler: handler})
	}
}

func (h *startupHTTPHandler) serveNonDiscovery(w http.ResponseWriter, r *http.Request) {
	if !h.ready.Load() {
		if r != nil && r.URL != nil && r.URL.Path == bootstrapdiscovery.HealthPath {
			bootstrapdiscovery.NotReadyHandler{}.ServeHTTP(w, r)
		} else {
			http.NotFound(w, r)
		}
		return
	}
	target, _ := h.next.Load().(*startupHandlerTarget)
	if target == nil || target.handler == nil {
		http.NotFound(w, r)
		return
	}
	target.handler.ServeHTTP(w, r)
}
