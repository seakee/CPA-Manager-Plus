package main

import (
	"context"
	"errors"
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
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshrouting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
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
	upgradeWizard     *upgradewizard.Handler
	upgradeArtifact   upgradewizard.ArtifactRuntime
	// discoveryObserver coalesces the concurrent unauthenticated observations of
	// the one source this process is looking at, and remembers the projection it
	// already proved for it while the source is unchanged.
	discoveryObserver *discoveryObservation
	cancelDiscovery   context.CancelFunc
	next              atomic.Value // *startupHandlerTarget
	lifecycle         atomic.Value // routingdomain.DiscoveryLifecycle
	ready             atomic.Bool
	fresh             atomic.Pointer[startupHandlerTarget]
}

type startupHandlerTarget struct {
	handler http.Handler
}

func newStartupHTTPHandler(result bootstrap.PublicDiscovery) *startupHTTPHandler {
	h := &startupHTTPHandler{
		projection:        result,
		readOnlyShell:     httpapi.BootstrapWebHandler(),
		discoveryObserver: &discoveryObservation{},
	}
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
// in a read-only shell. The separate Upgrade surface mounts its entry leaf, its
// two read-only wizard routes and the one verify action; no Bootstrap API is
// mounted, and the only write the surface can reach is the completion gate the
// action delegates to.
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
	shell := h.readOnlyShell
	h.projectionMu.RUnlock()
	if !readOnly || shell == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	shell.ServeHTTP(w, r)
}

// upgradeWizardNamespace is the one namespace the wizard owns on the fixed
// Bootstrap API. The namespace, not a list of exact resources, is what the fixed
// carrier routes to the reader: an unknown route inside it has to meet the same
// credential gate, so a caller cannot map the surface by asking for a path that
// does not exist. The reader answers 404 for an unknown route only after the
// credential and the browser's origin are decided, and it answers the verb rule
// itself: the two reads take GET and HEAD, the verify action takes POST.
const upgradeWizardNamespace = bootstrap.FixedBootstrapAPIPath + "installation/upgrade/"

// fixedUpgradeSurfaceLeaf reports whether a fixed-surface path belongs to the
// Upgrade surface: its authentication entry and every route in its namespace,
// all under the fixed Bootstrap carrier.
func fixedUpgradeSurfaceLeaf(value string) bool {
	return value == upgradeauth.Path || strings.HasPrefix(value, upgradeWizardNamespace)
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

// SetUpgradeArtifact attaches the Runtime Supervisor artifact client to the
// startup Upgrade surface so the wizard can observe and prepare the pinned CPA
// engine while an Upgrade operation is unfinished.
func (h *startupHTTPHandler) SetUpgradeArtifact(artifact upgradewizard.ArtifactRuntime) {
	h.projectionMu.Lock()
	defer h.projectionMu.Unlock()
	h.upgradeArtifact = artifact
	if h.upgradeWizard != nil {
		h.upgradeWizard.Artifact = artifact
	}
}

// EnableReadOnlyDiscovery projects only current, read-only evidence. Fresh or
// partial sources with no database are inventory-only observations. Existing
// databases may use a bounded rollback-mode SQLite read snapshot without
// creating a Manager marker; WAL/journal recovery remains unavailable here.
func (h *startupHTTPHandler) EnableReadOnlyDiscovery(paths sqliterepo.InspectionPaths, options legacypreflight.Options, timeout time.Duration) {
	h.enableReadOnlyDiscovery(paths, options, timeout, nil)
}

// EnableAdmittedDiscovery projects the inspection this process was already
// admitted on. The store was fenced, classified and read under the process lock
// before the surface was chosen, so its report is current evidence about the
// source — while the rollback-mode discovery snapshot would refuse the v2 WAL
// journal a running Manager writes, which is a fact about the snapshot and not
// about the source. Only a caller that holds that admission may pass a report
// here; every other shape keeps the fenced inspection and its fail-closed
// answer, so no WAL source outside a granted admission becomes observable.
func (h *startupHTTPHandler) EnableAdmittedDiscovery(paths sqliterepo.InspectionPaths, options legacypreflight.Options, timeout time.Duration, admitted legacypreflight.Report) {
	h.enableReadOnlyDiscovery(paths, options, timeout, &admitted)
}

func (h *startupHTTPHandler) enableReadOnlyDiscovery(paths sqliterepo.InspectionPaths, options legacypreflight.Options, timeout time.Duration, admitted *legacypreflight.Report) {
	h.projectionMu.Lock()
	defer h.projectionMu.Unlock()
	if h.cancelDiscovery != nil {
		h.cancelDiscovery()
		h.cancelDiscovery = nil
	}
	h.discoveryObserver.drain()
	// Entropy failure leaves this leaf closed without weakening discovery.
	h.upgradeAuth = nil
	h.upgradeWizard = nil
	// wizard reads verify the key against the same file, so a key the entry page
	// accepts is a key the reads accept, and the key kept in the other file is
	// refused by all three. The resolution also says which file it landed on,
	// because a preserved source and this process's own store are read
	// differently.
	// The configured 1.x data directory is resolved once and used twice: it is
	// the surface's credential rule's mount, and it is the second tree the
	// entry leaf must keep its private snapshot out of when the rule falls back
	// to the Manager store. Resolving it here, where the leaf is built, is what
	// lets the entry name the root on every authentication without a
	// per-request filesystem call it cannot bound.
	legacyRoot := configuredLegacyRoot()
	credentialPaths, mountSource := upgradeCredentialPaths(legacyRoot, paths)
	// A database this process was not admitted on is never its own live store:
	// before startup admission every read of paths.Database is a read of
	// somebody else's volume, and a 1.x volume mounted at /data with a WAL of
	// its own is exactly that. Both leaves must therefore prove the key and read
	// the recorded operation through a private snapshot; opening the file in
	// place would create or rewrite its -shm beside the source, and a read-only
	// mount would answer 503 for a source discovery just called upgradable.
	preserveManagerStore := admitted == nil
	if handler, err := upgradeauth.New(credentialPaths, os.Getenv("CPAMP_PUBLIC_ORIGIN"), mountSource, legacyRoot); err == nil {
		handler.SetPreserveManagerStore(preserveManagerStore)
		handler.SetManagerStorePaths(paths)
		h.upgradeAuth = handler
		// The wizard routes resolve the credential with the surface's one rule
		// from the mount they inspect and this database, exactly as the entry
		// leaf resolved it for `paths` at startup. They are mounted with the
		// leaf and closed with it: an unfinished Upgrade operation has no Fresh
		// record, so the fixed surface is the only carrier that can still show
		// what it recorded — and the only one that can run the completion gate.
		h.upgradeWizard = &upgradewizard.Handler{
			LegacyRoot:           configuredLegacyRoot,
			PublicOrigin:         strings.TrimSpace(os.Getenv("CPAMP_PUBLIC_ORIGIN")),
			Database:             func() string { return paths.Database },
			DataKey:              func() string { return paths.DataKey },
			Timeout:              upgradewizard.DefaultTimeout,
			Artifact:             h.upgradeArtifact,
			PreserveManagerStore: preserveManagerStore,
		}
	}
	if admitted != nil {
		// The projection is a pure function of the granted inspection, so it is
		// computed once and served without touching the store again.
		result := bootstrapdiscovery.Project(bootstrapdiscovery.Input{Report: *admitted})
		h.readOnlyDiscovery = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bootstrapdiscovery.Handler{Discovery: result}.ServeHTTP(w, r)
		})
		return
	}
	discoveryCtx, cancelDiscovery := context.WithCancel(context.Background())
	h.cancelDiscovery = cancelDiscovery
	h.readOnlyDiscovery = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The discovery document is a GET, and an unauthenticated caller of any
		// other method must be refused before this process stamps the source with
		// an observation it will never read.
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		// The observation this caller may start runs on its own lifetime, bounded
		// by the same timeout off a server context: a client that disconnects is
		// answered fail-closed by this call's own ctx, and the request still in
		// flight for the callers that joined it is neither abandoned nor turned
		// into their verdict. Every caller — the one that started it included —
		// writes its own response here, after its own ctx was honoured.
		h.discoveryObserver.observe(ctx, paths.Database,
			func() (bootstrap.PublicDiscovery, bool) {
				flightCtx, flightCancel := context.WithTimeout(discoveryCtx, timeout)
				defer flightCancel()
				if beforeDiscoveryObservation != nil {
					beforeDiscoveryObservation()
				}
				return observeReadOnlyDiscovery(flightCtx, paths, options, legacyRoot, func(bootstrap.PublicDiscovery) {})
			},
			func(result bootstrap.PublicDiscovery) {
				bootstrapdiscovery.Handler{Discovery: result}.ServeHTTP(w, r)
			})
	})
}

// observeReadOnlyDiscovery is the one read-only discovery of a source: the
// Phase4-01 inspection first, and the stopped 1.x journal observation when that
// inspection cannot describe the source. Each stage hands its verdict to consume
// while its own quiescence proof is still held — that is where the answer is
// proved, not where it is written — and the returned projection is what the
// shared flight publishes to every caller that joined it. The second result says
// whether that projection may be reused for an unchanged source.
func observeReadOnlyDiscovery(
	ctx context.Context,
	paths sqliterepo.InspectionPaths,
	options legacypreflight.Options,
	legacyRoot string,
	consume func(bootstrap.PublicDiscovery),
) (bootstrap.PublicDiscovery, bool) {
	// This path describes a source this process is not admitted on, so it takes
	// no ownership fence: a shared probe proves no writer holds the marker, works
	// where the marker cannot be opened for writing at all, and leaves the marker
	// the cache keys on untouched. A write fence here would chmod the source on
	// every unauthenticated request and would refresh the marker's change stamp,
	// which is exactly the identity the cache needs to stay stable.
	discoveryOptions := options
	discoveryOptions.ObserveOnly = true
	// Operator assertions are never inherited by public discovery: a source that
	// is claimed stopped is still only a rollback-mode read snapshot here.
	discoveryOptions.SourceStopped = false
	projection := bootstrapdiscovery.Unavailable()
	answered := false
	legacypreflight.WithFencedInspection(ctx, paths, discoveryOptions, func(report legacypreflight.Report, err error) {
		result := bootstrapdiscovery.Project(bootstrapdiscovery.Input{Report: report, InspectionError: err})
		if result.Status != bootstrap.PublicUnavailable || err == nil {
			answered = true
			projection = result
			consume(result)
		}
	})
	if answered {
		// A fail-closed projection is never remembered: it names this moment, not
		// the source, and the operator's next move is to change it.
		return projection, projection.Status != bootstrap.PublicUnavailable
	}
	// The rollback-mode inspection cannot describe a source whose committed rows
	// are still in a journal — every 1.x v1.14.x release runs in WAL mode — so
	// the fenced private copy answers instead, and its verdict is the one the
	// entry page needs before it can offer the 1.x administrator key.
	_, reusable := upgradeservice.InspectReadOnlyLegacySource(ctx, upgradeservice.PreservedSource{
		Database: paths.Database,
		DataKey:  paths.DataKey,
	}, legacyRoot, func(obs upgradeservice.LegacyDiscoveryObservation) {
		projection = legacyDiscoveryProjection(obs)
		consume(projection)
	})
	// A fail-closed projection names this moment, not the source: it is never
	// remembered, whichever stage produced it.
	return projection, reusable && projection.Status != bootstrap.PublicUnavailable
}

// legacyDiscoveryProjection maps the bounded legacy verdict onto the public
// contract. Only the conditions that name an operator action are projected; an
// unavailable answer stays unavailable.
func legacyDiscoveryProjection(obs upgradeservice.LegacyDiscoveryObservation) bootstrap.PublicDiscovery {
	switch obs {
	case upgradeservice.LegacyDiscoveryOwnershipUnavailable:
		return bootstrapdiscovery.SourceOwnershipUnavailable()
	case upgradeservice.LegacyDiscoveryUnsupportedUpgrade:
		return bootstrapdiscovery.UnsupportedUpgrade()
	case upgradeservice.LegacyDiscoveryRecoveryRequired:
		return bootstrapdiscovery.RecoveryRequired()
	default:
		return bootstrapdiscovery.Unavailable()
	}
}

// discoveryObservation coalesces the concurrent unauthenticated observations of
// one source and remembers the projection it produced while that source was
// unchanged.
//
// Both stages of the read-only path take the source's ownership marker for the
// duration of their read, so two overlapping requests refuse each other's fence
// and report a source nobody is writing as unavailable — the public port then
// looks unreachable for reasons that never touch the source. One flight per
// source removes that self-contention, and a projection that is a statement
// about the source's content is served again while LegacySourceIdentity is
// unchanged, so a poll does not copy the whole database again.
//
// A remembered projection grants nothing: it is the bounded public discovery
// document, and every write gate behind it — the Upgrade entry, the wizard's
// action and the preflight — fences the source again on its own.
type discoveryObservation struct {
	mu      sync.Mutex
	sources map[string]*discoveryObservationState
	flights sync.WaitGroup
	// ran counts the observations this instance actually executed, so a poll can
	// be shown to be answered from memory.
	ran int
}

type discoveryObservationState struct {
	flight *discoveryObservationFlight
	cached discoveryObservationCache
}

// discoveryObservationFlight is one observation in progress. A request that
// finds it waits for it and is answered with the projection it produced — the
// same answer the request that ran it received, whether that answer was a
// statement about the source or the fail-closed document of this moment. Sharing
// only successful observations would make each concurrent refusal of one busy
// source run its own full observation, which is the cost coalescing exists to
// remove.
type discoveryObservationFlight struct {
	done       chan struct{}
	projection bootstrap.PublicDiscovery
}

// discoveryObservationCache is the projection remembered for one source state.
// The identity is the source's metadata as it was after the read proved the
// source quiet, so any later write, checkpoint, permission change or
// replacement misses the cache and is inspected again.
type discoveryObservationCache struct {
	identity   string
	ready      bool
	projection bootstrap.PublicDiscovery
}

// afterCacheRevalidation is the package's synchronous test seam. It runs after
// the marker probe proved the source quiet and before a remembered projection is
// served, so a test can cancel the request at exactly the moment the answer is
// decided. Production never sets it.
var afterCacheRevalidation func()

// probeSource is the package's seam for that marker probe. Production never sets
// it; a test substitutes a failing probe to prove that a remembered projection is
// dropped rather than served when quiescence can no longer be proved.
var probeSource = processlock.ProbeExisting

// beforeDiscoveryObservation and afterDiscoveryFlightJoin are synchronous test
// seams for deterministic flight-cancellation tests. Production never sets them.
var (
	beforeDiscoveryObservation func()
	afterDiscoveryFlightJoin   func()
)

// observe runs read at most once at a time for one source. read proves its
// projection inside whatever fence it takes and returns it plus whether it may be
// reused for an unchanged source; it runs on a goroutine of its own, so the
// caller that started it can stop waiting without abandoning the callers that
// joined it. Every caller — starter and joiner alike — waits for the flight under
// its own ctx and writes its own answer through consume: one request's
// cancellation becomes that request's fail-closed document and never another
// caller's verdict. A caller that finds a remembered projection is served
// directly.
func (o *discoveryObservation) observe(
	ctx context.Context,
	databasePath string,
	read func() (bootstrap.PublicDiscovery, bool),
	consume func(bootstrap.PublicDiscovery),
) {
	// A canceled request is never answered from remembered evidence.
	if ctx.Err() != nil {
		consume(bootstrapdiscovery.Unavailable())
		return
	}
	identity, identified := upgradeservice.LegacySourceIdentity(databasePath)
	if identified {
		if projection, ok := o.cachedProjection(ctx, databasePath, identity); ok {
			// The probe above is a filesystem call, and the request may have
			// been canceled while it ran: a canceled caller is answered with
			// the fail-closed document, never with the evidence it asked for.
			if ctx.Err() != nil {
				consume(bootstrapdiscovery.Unavailable())
				return
			}
			consume(projection)
			return
		}
		if ctx.Err() != nil {
			consume(bootstrapdiscovery.Unavailable())
			return
		}
	}
	o.mu.Lock()
	state := o.sourceState(databasePath)
	if state.flight != nil {
		flight := state.flight
		o.mu.Unlock()
		if afterDiscoveryFlightJoin != nil {
			afterDiscoveryFlightJoin()
		}
		waitDiscoveryFlight(ctx, flight, consume)
		return
	}
	flight := &discoveryObservationFlight{
		done:       make(chan struct{}),
		projection: bootstrapdiscovery.Unavailable(),
	}
	state.flight = flight
	o.ran++
	o.flights.Add(1)
	o.mu.Unlock()

	go o.runFlight(databasePath, identified, identity, flight, read)
	waitDiscoveryFlight(ctx, flight, consume)
}

func (o *discoveryObservation) runFlight(
	databasePath string,
	identified bool,
	identity string,
	flight *discoveryObservationFlight,
	read func() (bootstrap.PublicDiscovery, bool),
) {
	projection := bootstrapdiscovery.Unavailable()
	reusable := false
	defer func() {
		if recovered := recover(); recovered != nil {
			projection = bootstrapdiscovery.Unavailable()
			reusable = false
		}
		o.mu.Lock()
		flight.projection = projection
		if reusable {
			if after, ok := upgradeservice.LegacySourceIdentity(databasePath); ok && (!identified || after == identity) {
				state := o.sourceState(databasePath)
				state.cached = discoveryObservationCache{identity: after, ready: true, projection: projection}
			}
		}
		o.sourceState(databasePath).flight = nil
		o.mu.Unlock()
		close(flight.done)
		o.flights.Done()
	}()
	projection, reusable = read()
}

func waitDiscoveryFlight(ctx context.Context, flight *discoveryObservationFlight, consume func(bootstrap.PublicDiscovery)) {
	select {
	case <-flight.done:
		if ctx.Err() != nil {
			consume(bootstrapdiscovery.Unavailable())
			return
		}
		consume(flight.projection)
	case <-ctx.Done():
		consume(bootstrapdiscovery.Unavailable())
	}
}

func (o *discoveryObservation) drain() {
	o.flights.Wait()
	o.mu.Lock()
	o.sources = nil
	o.mu.Unlock()
}

// cachedProjection answers from memory only while the source still looks the way
// the observation that produced it left it, and only while the marker proves what
// that observation proved. The check is a shared, non-blocking probe: it proves
// the same quiescence the observation proves, takes no write access, and changes
// nothing the identity is keyed on, so a poll costs one open instead of one copy.
//
// The remembered document is served only when the probe says nobody holds the
// marker — or that no marker exists at all, which is the Fresh and partial-source
// shape the observation itself accepts. Every other answer (a live writer, an
// unreadable marker, a replaced one, an I/O error) drops the entry and answers
// unavailable: the state the projection described can no longer be proved, and a
// stale 200 is exactly what this check exists to prevent.
func (o *discoveryObservation) cachedProjection(ctx context.Context, databasePath string, identity string) (bootstrap.PublicDiscovery, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	state := o.sourceState(databasePath)
	if !state.cached.ready || state.cached.identity != identity {
		return bootstrap.PublicDiscovery{}, false
	}
	probeErr := probeSource(databasePath)
	if afterCacheRevalidation != nil {
		afterCacheRevalidation()
	}
	if probeErr != nil && !errors.Is(probeErr, processlock.ErrNoExistingLock) {
		state.cached = discoveryObservationCache{}
		return bootstrapdiscovery.Unavailable(), true
	}
	if ctx.Err() != nil {
		return bootstrap.PublicDiscovery{}, false
	}
	return state.cached.projection, true
}

// observations reports how many fenced observations this instance chose to run.
// A memoised projection is served without adding to it, which is what a poll
// must not do.
func (o *discoveryObservation) observations() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.ran
}

func (o *discoveryObservation) sourceState(databasePath string) *discoveryObservationState {
	if o.sources == nil {
		o.sources = map[string]*discoveryObservationState{}
	}
	state, ok := o.sources[databasePath]
	if !ok {
		state = &discoveryObservationState{}
		o.sources[databasePath] = state
	}
	return state
}

// BeginWritableStartup drains pre-write projections before their evidence can
// become stale. It neither completes Bootstrap nor grants a new classification.
func (h *startupHTTPHandler) BeginWritableStartup() {
	h.projectionMu.Lock()
	defer h.projectionMu.Unlock()
	if h.cancelDiscovery != nil {
		h.cancelDiscovery()
		h.cancelDiscovery = nil
	}
	h.discoveryObserver.drain()
	h.projection = bootstrapdiscovery.Unavailable()
	h.readOnlyDiscovery = nil
	h.upgradeAuth = nil
	h.upgradeWizard = nil
	h.upgradeArtifact = nil
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
		switch {
		case r != nil && r.URL != nil && r.URL.Path == bootstrapdiscovery.HealthPath:
			bootstrapdiscovery.NotReadyHandler{}.ServeHTTP(w, r)
		case r != nil && r.URL != nil && r.URL.Path == freshrouting.AuthorityPath:
			h.serveStartupAuthority(w, r)
		default:
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

// serveStartupAuthority answers Ingress's private routing authority read while
// the startup listener owns HTTP traffic. It emits the positive pre-commit
// Bootstrap signal only when the fixed Bootstrap UI is actively serving 200;
// unready, writable-transition, failed-store, and retry windows return 503.
func (h *startupHTTPHandler) serveStartupAuthority(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.EscapedPath() != r.URL.Path || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.bootstrapUIAvailable(r.Context()) {
		writePreCommitBootstrapAuthority(w)
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
}

func (h *startupHTTPHandler) bootstrapUIAvailable(ctx context.Context) bool {
	if h.ready.Load() || h.discoveryLifecycle() != routingdomain.DiscoveryActive {
		return false
	}
	h.projectionMu.RLock()
	readOnly := h.readOnlyDiscovery != nil
	shell := h.readOnlyShell
	h.projectionMu.RUnlock()
	return readOnly && bootstrapShellReady(ctx, shell)
}
