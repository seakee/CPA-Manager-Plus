package main

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	routingdomain "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/routing"
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
	projectionMu      sync.RWMutex
	projection        bootstrap.PublicDiscovery
	readOnlyDiscovery http.Handler
	next              atomic.Value // *startupHandlerTarget
	lifecycle         atomic.Value // routingdomain.DiscoveryLifecycle
	ready             atomic.Bool
	fresh             atomic.Pointer[startupHandlerTarget]
}

type startupHandlerTarget struct {
	handler http.Handler
}

func newStartupHTTPHandler(result bootstrap.PublicDiscovery) *startupHTTPHandler {
	h := &startupHTTPHandler{projection: result}
	h.lifecycle.Store(routingdomain.DiscoveryActive)
	h.discovery = routingdomain.DiscoveryHandler{
		Lifecycle: h.discoveryLifecycle,
		Active:    http.HandlerFunc(h.serveDiscovery),
	}
	h.protected = httpapi.ProtectBrandResources(routingdomain.ProtectFixedDiscovery(http.HandlerFunc(h.serveNonDiscovery), h.discovery))
	h.next.Store(&startupHandlerTarget{handler: http.NotFoundHandler()})
	return h
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
