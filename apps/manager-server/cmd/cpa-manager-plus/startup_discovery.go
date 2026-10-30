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
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradewizard"
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
	upgradeWizard     http.Handler
	next              atomic.Value // *startupHandlerTarget
	lifecycle         atomic.Value // routingdomain.DiscoveryLifecycle
	ready             atomic.Bool
	fresh             atomic.Pointer[startupHandlerTarget]
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
// in a read-only shell. The separate Upgrade surface mounts its entry leaf and
// its two read-only wizard routes; no Bootstrap API or Upgrade writer is
// mounted.
func (h *startupHTTPHandler) serveFixedBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == bootstrap.FixedBootstrapDiscoveryPath {
		h.discovery.ServeHTTP(w, r)
		return
	}
	// The Upgrade surface owns one gate: not ready, discovery still active and
	// the Upgrade entry leaf present. Its entry authenticates only; its wizard
	// routes read the operation this process has not finished. An unfinished
	// operation has no Fresh record for the canonical continuation to carry, so
	// the two reads are mounted here, beside the entry leaf, on the one resolved
	// credential and with the same credential-before-verb order.
	if fixedUpgradeSurfaceLeaf(r.URL.Path) {
		h.projectionMu.RLock()
		defer h.projectionMu.RUnlock()
		if h.ready.Load() || h.discoveryLifecycle() != routingdomain.DiscoveryActive ||
			h.readOnlyDiscovery == nil || h.upgradeAuth == nil {
			http.NotFound(w, r)
			return
		}
		switch {
		case r.URL.Path == upgradeauth.Path:
			h.upgradeAuth.ServeHTTP(w, r)
		case h.upgradeWizard != nil:
			h.upgradeWizard.ServeHTTP(w, r)
		default:
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

// fixedUpgradeSurfaceLeaf reports whether a fixed-surface path belongs to the
// Upgrade surface: its authentication entry and its two read-only wizard
// routes, all under the fixed Bootstrap carrier.
func fixedUpgradeSurfaceLeaf(value string) bool {
	if value == upgradeauth.Path {
		return true
	}
	for _, suffix := range []string{upgradewizard.PreflightSuffix, upgradewizard.StatusSuffix} {
		if value == bootstrap.FixedBootstrapAPIPath+strings.TrimPrefix(suffix, "/") {
			return true
		}
	}
	return false
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
	h.upgradeWizard = nil
	// One credential resolution for the whole surface: the entry leaf and both
	// wizard reads verify the key against the same file, so a key the entry page
	// accepts is a key the reads accept, and the key kept in the other file is
	// refused by all three.
	if handler, err := upgradeauth.New(upgradeCredentialPaths(paths), os.Getenv("CPAMP_PUBLIC_ORIGIN")); err == nil {
		h.upgradeAuth = handler
		// The wizard routes resolve the credential with the surface's one rule
		// from the mount they inspect and this database, exactly as the entry
		// leaf resolved it for `paths` at startup. They are mounted with the
		// leaf and closed with it: an unfinished Upgrade operation has no Fresh
		// record, so the fixed surface is the only carrier that can still show
		// what it recorded.
		h.upgradeWizard = upgradewizard.Handler{
			LegacyRoot:   configuredLegacyRoot,
			PublicOrigin: strings.TrimSpace(os.Getenv("CPAMP_PUBLIC_ORIGIN")),
			Database:     func() string { return paths.Database },
			Timeout:      upgradewizard.DefaultTimeout,
		}
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
	h.upgradeWizard = nil
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
