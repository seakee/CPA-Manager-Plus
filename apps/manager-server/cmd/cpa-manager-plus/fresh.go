package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityprojection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityreconcile"
	usageapp "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/routing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/freshbootstrap"
	healthcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/health"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradewizard"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/router"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	keyrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	cpaupdateservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaupdate"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshrouting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	proxysvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/proxy"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/updatecheck"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/worker"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// initialize is a host-owned offline operation. It admits the actual store and
// returns a bounded fragment handoff to its caller, never a service log or a
// second long-running daemon. The normal service starts/resumes the same store.
func runFreshInitialize(ctx context.Context, stdout io.Writer) error {
	cfg, err := config.LoadForInspection()
	if err != nil {
		return err
	}
	if err := validateFreshHostAuthority(cfg); err != nil {
		return err
	}
	paths, err := freshPaths(cfg)
	if err != nil {
		return err
	}
	opened, err := fresh.Open(ctx, paths, fresh.OpenOptions{Initialize: true, AdminKey: cfg.AdminKey, DataKey: cfg.DataKey})
	if err != nil {
		if classified, ok := fresh.ClassifyInitializeRefusal(ctx, paths.Database, err); ok {
			return classified
		}
		return err
	}
	defer opened.Close()
	if opened.Record.Operation.ProductComplete {
		return fresh.AlreadyCompleteError{ControlBasePath: opened.Record.Operation.Routing.Target}
	}
	if opened.Record.Operation.Routing.Committed {
		return fresh.RoutingCommittedError{ControlBasePath: opened.Record.Operation.Routing.Target}
	}
	handoff, err := fresh.New(freshstore.New(opened.DB)).Issue(ctx)
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, bootstrap.FixedBootstrapUIPath+handoff.Fragment+"\n")
	return err
}

func freshInitializeErrorMessage(err error) string {
	var complete fresh.AlreadyCompleteError
	if errors.As(err, &complete) {
		if routing.ValidControlBasePath(complete.ControlBasePath) {
			return fmt.Sprintf("Installation is already complete; open the console at %s with your administrator key (no Bootstrap handoff is needed)", complete.ControlBasePath)
		}
		return "Installation is already complete; open the console with your administrator key (no Bootstrap handoff is needed)"
	}
	var committed fresh.RoutingCommittedError
	if errors.As(err, &committed) {
		if routing.ValidControlBasePath(committed.ControlBasePath) {
			return fmt.Sprintf("Initial console route %s is already active; open %s with your administrator key to finish setup (no Bootstrap handoff is needed)", committed.ControlBasePath, committed.ControlBasePath)
		}
		return "Initial console route is already active; open the console with your administrator key to finish setup (no Bootstrap handoff is needed)"
	}
	if errors.Is(err, fresh.ErrStoreLockedInProgress) {
		return "Fresh installation is not finished and the Manager service holds the store lock; stop the writable Manager service before running initialize to issue a resume handoff"
	}
	return "Fresh initialization/handoff unavailable; inspect the host source and ownership before retrying"
}

func freshPaths(cfg config.Config) (sqliterepo.InspectionPaths, error) {
	configPath, err := config.InspectionConfigPath()
	return sqliterepo.InspectionPaths{Database: cfg.DBPath, Config: configPath, DataKey: cfg.DataKeyPath, Archives: cfg.UsageArchiveDir}, err
}

var errFreshExternalPermitRevoked = errors.New("fresh external host permit revoked")

func isCommittedExternalStrategy(c bootstrap.FreshStrategyCheckpoint) bool {
	return c.Strategy == bootstrap.ConnectExternal && c.Stage == "committed"
}

// runtimeLifecycleMutex is a context-cancellable 1-slot semaphore shared
// between explicit operator lifecycle mutations and automatic reconcile cycles.
type runtimeLifecycleMutex struct {
	initOnce sync.Once
	ch       chan struct{}
	onWait   func()
}

func (m *runtimeLifecycleMutex) tokenCh() chan struct{} {
	m.initOnce.Do(func() {
		m.ch = make(chan struct{}, 1)
		m.ch <- struct{}{}
	})
	return m.ch
}

func (m *runtimeLifecycleMutex) unlockFunc(ch chan struct{}) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			ch <- struct{}{}
		})
	}
}

func (m *runtimeLifecycleMutex) Lock(ctx context.Context) (func(), error) {
	if m == nil {
		return func() {}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ch := m.tokenCh()
	select {
	case <-ch:
		if err := ctx.Err(); err != nil {
			ch <- struct{}{}
			return nil, err
		}
		return m.unlockFunc(ch), nil
	default:
	}
	if m.onWait != nil {
		m.onWait()
	}
	select {
	case <-ch:
		if err := ctx.Err(); err != nil {
			ch <- struct{}{}
			return nil, err
		}
		return m.unlockFunc(ch), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type freshApplication struct {
	cfg                config.Config
	store              freshstore.Store
	authority          *fresh.Service
	routing            *freshrouting.Service
	strategy           *freshstrategy.Service
	runtime            runtimeservice.RuntimeClient
	artifact           *runtimeservice.ArtifactClient
	runtimeExternal    *runtimeservice.ExternalClient
	embeddedModeLock   sync.Mutex
	embeddedExecution  freshEmbeddedExecutionGate
	desiredState       runtimeservice.DesiredStateStore
	runtimeSettings    setting.Repository
	runtimeJournal     *freshRuntimeJournal
	runtimeLifecycleMu runtimeLifecycleMutex
	externalPermit     func(bootstrap.FreshOperation, uint64, string, string, string) bool
	protector          *security.Protector
	fixed              http.Handler
	keyRepo            *keyrepo.Repository
	keys               *freshkeys.Service
	keyApply           *freshkeyapply.Service
	updates            *updatecheck.Service
	gatewayConfig      *freshconfig.Service
	// pluginCapability is the short-lived proof behind X-CPA-SUPPORT-PLUGIN.
	// It is nil until the application knows which connection it serves.
	pluginCapability *pluginCapabilityProbe
	legacy           http.Handler
	legacyApp        *app.Context
	// upgradeWizard is the one Upgrade reader this application serves. It is
	// built once: the handler owns the per-request mount flight and the one
	// bounded lookup a poll joins, so constructing one per request would give
	// every request its own flight and its own filesystem calls.
	upgradeWizard *upgradewizard.Handler
	// legacyCredentialGate serializes the legacy requests that present the saved
	// management credential. A console page opens several at once, and five
	// failed authentications ban this address, so the first refusal has to be
	// recorded before the next request leaves. It is held only until the answer's
	// status is known, and a waiter whose own request is cancelled gives up.
	legacyCredentialGate externalCredentialGate
	// credentialHeaderBudget is how long one gated request may hold the credential
	// gate waiting for the CPA's status. It exists for the hung CPA: a process that
	// accepts the connection and never answers must not stall every other console
	// read. Zero means externalCredentialHeaderBudgetDefault.
	credentialHeaderBudget time.Duration
	afterCredentialHoldBudgetExpiry func()
	legacyCollector        *collectorservice.Service
	collectorWorker        *worker.CollectorWorker
	identityWorker         *identityreconcile.Worker
	// Usage chain: staged keys own source bindings, Collector events feed the
	// canonical identity projection, and the workspace reads attributed usage.
	managedSource   freshkeyapply.ManagedSource
	usageProjection *identityprojection.Worker
	usage           *usageapp.Service
	// usageStartMu serializes post-completion startup attempts and guards the
	// two success flags below. Only a fully successful attempt is recorded, so a
	// failed start is retried by a completion replay or a later trigger.
	usageStartMu                     sync.Mutex
	beforeUsageReconcileLock         func()
	beforeCollectorAuthRejectionLock func(runID uint64)
	afterCollectorAuthRejection      func(runID uint64)
	afterExternalCollectorStart      func()
	usageStarted                     bool
	usageWorkersStarted              bool
	usageCollectorStarted            bool
	usageAwaitCancel                 context.CancelFunc
	usageAwaitID                     uint64
	// usageStartReason is the stable code of the last refused Collector start.
	// It is empty once a start succeeded, so the workspace can tell an unstarted
	// window from one that was never attempted.
	usageStartReason    atomic.Value
	activeExternalGrant atomic.Value
	// externalUsageConfirmed records the administrator's explicit confirmation
	// to enable usage statistics on the user's own CPA. Without it nothing is
	// written to that CPA and the collection window never opens. It is
	// authorisation for one write and is consumed by the attempt that uses it,
	// so a later replay or a restart asks again.
	externalUsageConfirmed atomic.Bool
	// runCtx is the host lifetime of this application. Workers started after an
	// in-process ProductComplete must outlive the request that triggered them.
	runCtx context.Context
	// privateCollector is the host-supplied Collector authority of the explicit
	// private profile. It stays nil for Managed and External installs, which may
	// never fall back to the legacy manager/setup authority.
	privateCollector func(context.Context) bool
	// startIdentityWorker starts the Embedded identity reconciliation loop; a
	// field so the completion path can be proven to start it once.
	startIdentityWorker func(context.Context)
	// externalSource is the committed Connect External authority. It is the only
	// source of an External management credential; no environment fallback exists.
	externalSource freshkeyapply.ExternalSource
	// request: adoptMigrationMu makes the start single-flight per installation
	// and guards the flag that says a run is in flight.
	adoptMigrationMu      sync.Mutex
	adoptMigrationRunning bool
	// rejection remembers that the user's CPA refused the stored External
	// credential. Browser-driven reads consult it before they send anything: CPA
	// bans an address after five failed authentications, so a page that keeps
	// presenting a key its own CPA already rejected is what turns a rotation into
	// a thirty-minute outage.
	rejection *externalRejection
}

func newFreshApplication(opened *fresh.Opened, cfg config.Config, driver freshrouting.Driver) *freshApplication {
	st := freshstore.New(opened.DB)
	settings := setting.New(opened.DB, opened.Protector)
	a := &freshApplication{
		cfg: cfg, store: st, authority: fresh.New(st), routing: freshrouting.New(st, driver),
		protector:       opened.Protector,
		runtimeSettings: settings, runtimeJournal: newFreshRuntimeJournal(opened.DB),
		rejection: newExternalRejection(opened.DB),
	}
	// Fresh owns the SQLite admission and keeps the old app bootstrap bypassed,
	// but the already-built CPAMP services remain the source of truth for Full
	// Web's shared Dashboard/Config/System resources. This context reuses the
	// admitted connection. The Collector it builds starts only after
	// ProductComplete, when the committed install-derived CPA authority exists;
	// pending installs cannot start collection through an old host route.
	legacyStore := store.New(opened.DB, opened.Protector)
	if err := legacyStore.RunDerivedStartupMaintenance(context.Background()); err != nil {
		log.Printf("[startup] fresh post-open index preparation failed; continuing without blocking background workers: %v", err)
	}
	legacyCollector := collector.NewManager(cfg, legacyStore)
	legacyCollectorService := collectorservice.New(legacyCollector)
	legacyContext := app.FromExistingWithModelsDev(cfg, legacyStore, legacyCollector, time.Now().UnixMilli(), nil, nil, nil, nil, "cpa-manager-plus")
	a.legacyCollector = legacyCollectorService
	a.legacyApp = legacyContext
	// Fresh owns Collector lifecycle on a.runCtx: narrow monitoring-switch PATCH
	// updates only Manager's DB settings row, ConnectExternal blocks whole-config
	// PUT and skips Get's optional CPA usage probe (External posture lives on
	// GET external/usage), and Managed/Adopt reconcile the host-bound Collector
	// after PUT.
	legacyContext.ManagerConfigService.UseExternalCollectorLifecycle()
	legacyContext.ManagerConfigService.SetCPAUsageProbeAllowed(func(probeCtx context.Context) bool {
		if a.store == nil {
			return true
		}
		record, loadErr := a.store.Load(probeCtx)
		if loadErr != nil {
			return false
		}
		return !isCommittedExternalStrategy(record.Operation.Strategy)
	})
	if dbPath := strings.TrimSpace(opened.Record.Operation.Identity.DatabasePath); dbPath != "" {
		legacyContext.DatabaseMaintenance = freshDatabaseStatusProvider{dbPath: dbPath}
	}
	a.collectorWorker = worker.NewCollectorWorker(cfg, legacyStore, legacyCollectorService)
	// A CPA that refuses the management credential this Collector runs with is
	// terminal, not a retry: the Collector stops itself and reports, and this
	// application records the blocker and drops the started latch so the
	// workspace stops claiming coverage.
	legacyCollector.SetAuthRejectionRunHandler(a.reportCollectorAuthRejection)
	// The Collector is the only writer of Fresh usage events, so its inserts are
	// exactly what the canonical identity projection must attribute.
	a.usageProjection = identityprojection.NewWorker(legacyStore.IdentityProjections)
	legacyCollector.SetUsageEventHandler(collector.UsageEventHandlerFunc(func(ctx context.Context, _ collector.RuntimeConfig, events []usage.Event) {
		a.usageProjection.HandleUsageEvents(ctx, events)
	}))
	a.usage = usageapp.New(identityusage.New(opened.DB), a.collectionPhase)
	a.keyRepo = keyrepo.New(opened.DB)
	a.keys = freshkeys.New(a.keyRepo, opened.Protector, freshkeys.WithSourceRuntime(a.installedRuntimeIdentity))
	// Reuse the existing settings authority without opening a Legacy app,
	// collector, CPA client, or second SQLite connection.
	a.updates = updatecheck.New(store.New(opened.DB), buildinfo.Version, buildinfo.SourceCommit, os.Getenv("CPAMP_UPDATE_CHECK_ENABLED") != "false")
	var runtime runtimeservice.RuntimeClient
	// externalConnection is the Manager-configured External CPA. It is the only
	// source of an External management credential; no environment fallback exists.
	externalConnection := func(ctx context.Context) (string, string, error) {
		if a.store != nil && a.externalSource != nil {
			install, err := a.externalSource(ctx)
			if err != nil {
				return "", "", err
			}
			return install.BaseURL, install.ManagementKey, nil
		}
		managerCfg, found, err := settings.LoadManagerConfig(ctx)
		if err != nil {
			return "", "", err
		}
		if !found {
			return "", "", errors.New("CPA connection is not configured")
		}
		return managerCfg.CPAConnection.CPABaseURL, managerCfg.CPAConnection.ManagementKey, nil
	}
	alreadyExternal := isCommittedExternalStrategy(opened.Record.Operation.Strategy)
	a.runtimeExternal = runtimeservice.NewExternalClientWithConnectionSource(externalConnection)
	if cfg.EmbeddedRuntimeConfigured() && !alreadyExternal {
		tokenSource := runtimeservice.NewFileRuntimeTokenSource(cfg.RuntimeTokenFile)
		runtime = runtimeservice.NewEmbeddedClientWithTokenSource(cfg.RuntimeURL, tokenSource)
		a.artifact = runtimeservice.NewArtifactClientWithTokenSource(cfg.RuntimeURL, tokenSource)
	}
	a.runtime = runtime
	legacyContext.CPAUpdateService = cpaupdateservice.New(
		legacyStore,
		freshRuntimeClientAdapter{app: a},
		a.runtimeModeFor(opened.Record.Operation),
	).WithModeResolver(func() model.RuntimeMode {
		return a.effectiveRuntimeMode(context.Background())
	})
	// Attach the same durable mutation services as the normal Embedded app
	// before publishing its routes. An incomplete graph must never fall back to
	// unjournaled API-key rotation or credential deletion; a committed External
	// install owns no Supervisor-managed CPA and never wires Embedded fences.
	var err error
	if !alreadyExternal {
		var identityWiring freshIdentityWiring
		identityWiring, err = wireFreshIdentityMutations(legacyContext, a.embeddedRuntimeClient())
		if err != nil {
			log.Printf("Fresh Console mutation services unavailable: %v", err)
		} else {
			a.identityWorker, err = newFreshIdentityReconcileWorker(legacyContext, identityWiring)
			if err != nil {
				log.Printf("Fresh identity reconciliation unavailable: %v", err)
			} else if worker := a.identityWorker; worker != nil {
				a.startIdentityWorker = func(ctx context.Context) { go worker.Run(ctx) }
			}
		}
	}
	// Embedded proxy mutation fences run only when the durable strategy checkpoint
	// is read and proves a Manager-owned installation. Checkpoint read errors or
	// uncommitted strategies fail closed with 503 so neither profile executes an
	// unverified or misrouted mutation.
	legacyContext.ProxyService.SetEmbeddedMutationsEnabled(func(ctx context.Context) (bool, error) {
		op, loadErr := a.current(ctx)
		if loadErr != nil {
			return false, loadErr
		}
		switch {
		case isCommittedExternalStrategy(op.Strategy):
			return false, nil
		case freshkeyapply.ManagerOwnedStrategy(op.Strategy):
			return true, nil
		default:
			return false, errors.New("installation strategy is not committed")
		}
	})
	// An unavailable Embedded writer must not remove unrelated Console reads,
	// and External has no identity worker to install. Always construct the
	// router, but fail writes closed if either mutation wiring step failed, and
	// on External refuse the writes Fresh already owns plus any request that
	// would present a credential its CPA already rejected.
	a.legacy = guardFreshExternalLegacy(guardFreshLegacyWrites(router.New(legacyContext), err), a)
	// The private host launcher alone mounts the short-lived permit. The default
	// v7 profile and existing environments never opt into this consumer.
	h1Mode := os.Getenv("CPAMP_PRIVATE_H1")
	h1Valid := h1Mode == "" || (h1Mode == "1" && os.Getenv("CPAMP_PRIVATE_CONFIG_AUTHORITY") == "1" && os.Getenv("CPAMP_PRIVATE_KEY_OPERATIONS") == "1")
	if h1Valid && os.Getenv("CPAMP_PRIVATE_S1_PERMIT") == freshkeyapply.PermitPath && cfg.RuntimeURL == "http://127.0.0.1:9081" && cfg.CPAUpstreamURL == "http://127.0.0.1:8317" {
		a.keyApply = freshkeyapply.New(a.keyRepo, a.keys, a.embeddedRuntimeClient())
		a.gatewayConfig = freshconfig.New(st, a.keyApply.ConfigTarget, a.keyApply.TargetsFunc(freshkeyapply.SurfaceGatewayConfig))
		// The explicit private profile keeps its own host-supplied Collector
		// authority. Managed and External installs never reach this branch and
		// therefore never fall back to it.
		a.privateCollector = func(ctx context.Context) bool {
			if a.collectorWorker == nil {
				return false
			}
			a.collectorWorker.Start(ctx)
			return a.collectionPhase() != usageapp.PhaseStopped
		}
		if os.Getenv("CPAMP_PRIVATE_H1") == "1" && os.Getenv("CPAMP_PRIVATE_CONFIG_AUTHORITY") == "1" && os.Getenv("CPAMP_PRIVATE_KEY_OPERATIONS") == "1" {
			h := cpa.NewH1Coordinator(opened.DB)
			a.keyApply.EnableH1(h)
			a.gatewayConfig.EnableH1(h)
		}
	}
	if a.keyApply == nil && h1Valid {
		// Select Managed vs External from the durable Fresh strategy checkpoint
		// rather than from the presence of an Embedded runtime client.
		supervisorRoots := cfg.TrustedSupervisorRoots()
		storeView := freshStoreView{app: a}
		a.managedSource = managedInstallSource(storeView, opened.Protector, cfg.CPAUpstreamURL)
		a.externalSource = externalInstallSource(storeView, opened.Protector)
		managedAuthority := freshkeyapply.NewManaged(
			a.keyRepo,
			a.keys,
			gatedEmbeddedRuntimeClient{app: a},
			cfg.CPAUpstreamURL,
			func(ctx context.Context) (freshkeyapply.ManagedInstall, error) {
				if a.managedSource == nil {
					return freshkeyapply.ManagedInstall{}, freshkeyapply.ErrUnavailable
				}
				return a.managedSource(ctx)
			},
		)
		managedAuthority.SetTrustedSupervisorRoots(supervisorRoots)
		externalAuthority := freshkeyapply.NewExternal(
			a.keyRepo,
			a.keys,
			func(ctx context.Context) (freshkeyapply.ExternalInstall, error) {
				if a.externalSource == nil {
					return freshkeyapply.ExternalInstall{}, freshkeyapply.ErrExternalUnavailable
				}
				return a.externalSource(ctx)
			},
		)
		a.keyApply = freshkeyapply.NewStrategySelector(
			func(op bootstrap.FreshOperation) *freshkeyapply.Service {
				switch {
				case isCommittedExternalStrategy(op.Strategy):
					return externalAuthority
				case freshkeyapply.ManagerOwnedStrategy(op.Strategy):
					return managedAuthority
				default:
					return nil
				}
			},
			func() (bootstrap.FreshOperation, bool) {
				op, loadErr := a.current(context.Background())
				return op, loadErr == nil
			},
		)
		a.gatewayConfig = freshconfig.New(storeView, a.keyApply.ConfigTarget, a.keyApply.TargetsFunc(freshkeyapply.SurfaceGatewayConfig))
		a.gatewayConfig.EnableManaged(cfg.CPAUpstreamURL)
		a.gatewayConfig.SetTrustedSupervisorRoots(supervisorRoots)
		// New Managed and Adopt prove different destination postures, so the page
		// asks the same committed checkpoint the key authority asks.
		a.gatewayConfig.SetPostureSelector(func() (cpa.DestinationPosture, bool) {
			if a.managedSource == nil {
				return cpa.DestinationPosture{}, false
			}
			install, err := a.managedSource(context.Background())
			if err != nil {
				return cpa.DestinationPosture{}, false
			}
			return cpa.PostureForWithSupervisorRoots(install.Strategy, supervisorRoots)
		})
		a.gatewayConfig.EnableExternal(a.keyApply.ExternalTarget)
	}
	a.desiredState = a.desiredStateStore()
	// The plugin capability the navigation reads is proven, not assumed: this
	// installation must be able to show that its saved connection serves the
	// plugin namespace. A managed install owns that answer through its committed
	// destination, and any other profile must prove it from CPA's own document.
	if a.gatewayConfig != nil {
		a.pluginCapability = newPluginCapabilityProbe(pluginCapabilityTTL, pluginCapabilityProbeTimeout, a.v8PluginCapable)
	}
	// Only the build-bound consumer record can grant production qualification.
	// Pending/missing evidence retains the existing typed strategy blockers. The
	// strategy's post-Start destination read-back must not race the spawn, so its
	// Runtime client waits for the Supervisor's readiness observation first.
	a.strategy = freshstrategy.NewProduction(freshstrategy.Options{Runtime: freshReadyStartRuntime(a.embeddedRuntimeClient()), Protector: opened.Protector, DestinationBaseURL: cfg.CPAUpstreamURL, InitialClientKey: a.installInitialClientKey})
	// Normal Fresh initialization is authorized by the admitted installation
	// and its Admin/session. Retain the opt-in private harness restriction only
	// for hosts explicitly running that profile.
	if os.Getenv("CPAMP_PRIVATE_A3_PROFILE") != "" {
		a.externalPermit = privateFreshExternalAdmitted
	}
	// One Upgrade surface for the whole application: both carriers serve it, and
	// its per-handler mount flight is what keeps a hung mount to one lookup. The
	// two reads and the one verify action share that handler, so the page and
	// the command line cannot disagree about whether the upgrade finished.
	a.upgradeWizard = a.newUpgradeWizard()
	a.fixed = freshbootstrap.Handler{Authority: a.authority, Routing: a.routing, Current: a.current,
		Shell: httpapi.BootstrapWebHandler(), Strategy: http.HandlerFunc(a.fixedStrategy), Runtime: http.HandlerFunc(a.fixedRuntime),
		Adoption: http.HandlerFunc(a.fixedAdoption), Artifact: http.HandlerFunc(a.fixedArtifact),
		Upgrade: a.upgradeWizard}
	return a
}

func (a *freshApplication) current(ctx context.Context) (bootstrap.FreshOperation, error) {
	r, err := a.store.Load(ctx)
	return r.Operation, err
}

func writeFreshStrategyError(w http.ResponseWriter, err error) {
	var actionBlocker *freshStrategyActionBlocker
	if errors.As(err, &actionBlocker) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		writeFreshJSON(w, actionBlocker)
		return
	}
	var blocker *freshstrategy.Blocker
	if errors.As(err, &blocker) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		writeFreshJSON(w, blocker)
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
}

func (a *freshApplication) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The plugin capability is a claim about the saved connection, so it is
	// announced only when that connection proves it; a route existing is not a
	// capability.
	withSupportPluginHeader(http.HandlerFunc(a.serve), a.supportPluginCapable).ServeHTTP(w, r)
}

func (a *freshApplication) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r != nil && r.URL != nil && r.URL.Path == bootstrap.FixedLivenessPath {
		// Fresh store takeover makes the Manager ready, not the installation or
		// Gateway. Re-read the validated store so a later failure is not healthy.
		if _, err := a.current(r.Context()); err != nil {
			bootstrapdiscovery.NotReadyHandler{}.ServeHTTP(w, r)
		} else {
			handler := healthcontroller.Handler{ServiceID: "cpa-manager-plus"}
			handler.Health(w, r)
		}
		return
	}
	if r != nil && r.URL != nil && r.URL.Path == privateChatAdmissionPath {
		a.privateChatAdmission(w, r)
		return
	}
	if r != nil && r.URL != nil && r.URL.Path == freshrouting.AuthorityPath && r.URL.EscapedPath() == r.URL.Path && r.URL.RawQuery == "" {
		a.freshAuthority(w, r)
		return
	}
	if r != nil && r.URL != nil && r.URL.Path == privateStagedKeyProofPath {
		a.privateStagedKeyProof(w, r)
		return
	}
	if a.legacy != nil && r != nil && r.URL != nil && pluginResourceRequest(r) {
		// Plugin pages must not be answered by the Full Web document handler:
		// they are proxied so ProxyPluginResource can enforce the opaque-origin
		// sandbox contract on every response.
		a.legacy.ServeHTTP(w, r)
		return
	}
	protect := routing.ProtectFixedBootstrap(http.HandlerFunc(a.canonical), a.fixed)
	// The canonical surface is Full Web's admin API. A sandboxed plugin document
	// is a cross-site fetch client, so it is refused here as well as in the
	// legacy router that serves the Foundation-era pages.
	httpapi.ProtectBrandResources(middleware.RejectCrossSiteAdmin(protect, proxysvc.IsCanonicalPluginResourceRequest)).ServeHTTP(w, r)
}

func (a *freshApplication) canonical(w http.ResponseWriter, r *http.Request) {
	op, err := a.current(r.Context())
	if err != nil {
		if errors.Is(err, freshstore.ErrNotFresh) {
			http.NotFound(w, r)
			return
		}
		writeFreshCanonicalStoreUnavailable(w, r)
		return
	}
	if op.Routing.Target == "" {
		http.NotFound(w, r)
		return
	}
	api := http.HandlerFunc(a.canonicalAPI)
	if op.Routing.Committed {
		snapshot := a.routing.Snapshot(r.Context())
		if !snapshot.PublicAPIActive() && snapshot.State == routing.SnapshotUnavailable {
			snapshot = a.routing.Snapshot(r.Context())
		}
		if !snapshot.PublicAPIActive() {
			if snapshot.State == routing.SnapshotUnavailable {
				reloaded, loadErr := a.store.Load(r.Context())
				if loadErr != nil && !errors.Is(loadErr, freshstore.ErrNotFresh) ||
					loadErr == nil && reloaded.Operation.Routing.Committed {
					writeFreshCanonicalStoreUnavailable(w, r)
					return
				}
			}
			http.NotFound(w, r)
			return
		}
		httpapi.FullWebHandler(snapshot, api).ServeHTTP(w, r)
		return
	}
	// The pending route is explicitly owned by the existing Fresh checkpoint;
	// it is a verification candidate and never a verified-effective snapshot.
	httpapi.InitialFullWebHandler(op.Routing.Target, api).ServeHTTP(w, r)
}

func writeFreshCanonicalStoreUnavailable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if isCredentialedCanonicalComplete(r) {
		writeFreshAdoptCompleteError(w, http.StatusServiceUnavailable, "adopt_installation_unavailable", 0)
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
}

func isCredentialedCanonicalComplete(r *http.Request) bool {
	if r == nil || r.URL == nil || r.Method != http.MethodPost {
		return false
	}
	const completeSuffix = routing.PublicAPIV2Suffix + "/complete"
	if !strings.HasSuffix(r.URL.Path, completeSuffix) {
		return false
	}
	prefix := strings.TrimSuffix(r.URL.Path, completeSuffix)
	if !routing.ValidControlBasePath(prefix) {
		return false
	}
	_, freshAuth := freshbootstrap.SessionToken(r)
	return freshAuth || security.ExtractBearerToken(r.Header.Get("Authorization")) != ""
}

func (a *freshApplication) canonicalAPI(w http.ResponseWriter, r *http.Request) {
	// Full Web's HTML/static wrapper sets no-cache. Restore the API boundary
	// here before any authentication error or one-time secret response.
	w.Header().Set("Cache-Control", "no-store")
	record, err := a.store.Load(r.Context())
	if err != nil {
		if errors.Is(err, freshstore.ErrNotFresh) {
			http.NotFound(w, r)
			return
		}
		writeFreshCanonicalStoreUnavailable(w, r)
		return
	}
	op := record.Operation
	base := op.Routing.Target + routing.PublicAPIV2Suffix + "/"
	resource := strings.TrimPrefix(r.URL.Path, base)
	if resource == r.URL.Path {
		http.NotFound(w, r)
		return
	}
	// Non-secret identity is the bounded real-public-entry verification leaf.
	// It authenticates no browser and cannot mutate the pending installation.
	if resource == "bootstrap/identity" && r.Method == http.MethodGet {
		writeFreshJSON(w, freshrouting.StateFor(op))
		return
	}
	// The Upgrade reads own their credential. They are neither the installation
	// session's nor the Fresh Admin's, so they are answered before either gate and
	// the reader decides the caller. A completed installation has no Upgrade step
	// at all, so the resources stop existing with the operation.
	if upgradeWizardResource(resource) {
		if op.ProductComplete {
			http.NotFound(w, r)
			return
		}
		a.upgradeWizard.ServeHTTP(w, r)
		return
	}
	continuation, freshAuth := freshbootstrap.SessionToken(r)
	if freshAuth {
		// The existing claim may finish its own installation after route
		// cutover, but is never an Admin credential for the Console/CPA proxy.
		allowed := resource == "operation" && r.Method == http.MethodGet || !op.ProductComplete &&
			(resource == "installation/runtime" && r.Method == http.MethodGet ||
				// The per-item migration summary is the progress read of an
				// adoption's own run, so the session that committed the
				// adoption reads it exactly as it reads the runtime
				// observation.
				resource == "installation/adoption" && r.Method == http.MethodGet ||
				runtimeArtifactResource(resource) ||
				(resource == "strategy" || resource == "complete") && r.Method == http.MethodPost)
		if !allowed || a.authority.ValidateContinuation(record, continuation) != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	} else if !op.Routing.Committed || record.Admin == nil || !security.VerifyAdminKey(*record.Admin, security.ExtractBearerToken(r.Header.Get("Authorization"))) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	// The legacy CPAMP router is a post-installation resource. Reusing the
	// Fresh Admin credential is not enough to authorize it while the selected
	// strategy is still staged or being recovered: those writes must remain
	// behind the Fresh operation/journal/permit boundary until completion.
	if strings.HasPrefix(resource, "cpamp/") && !op.ProductComplete {
		w.WriteHeader(http.StatusPreconditionRequired)
		return
	}
	switch {
	case resource == "gateway/config" || strings.HasPrefix(resource, "gateway/config/"):
		a.gatewayConfiguration(w, r, strings.TrimPrefix(strings.TrimPrefix(resource, "gateway/config"), "/"))
	case resource == "plugins/runtime" || strings.HasPrefix(resource, "plugins/runtime/"):
		a.pluginRuntime(w, r, strings.TrimPrefix(strings.TrimPrefix(resource, "plugins/runtime"), "/"))
	case resource == "settings/update-channel":
		a.updateChannel(w, r, op)
	case resource == "api-keys" || strings.HasPrefix(resource, "api-keys/"):
		a.clientKeys(w, r, op, resource)
	case resource == "external/usage" || strings.HasPrefix(resource, "external/usage/"):
		a.externalUsageRoute(w, r, op, strings.TrimPrefix(strings.TrimPrefix(resource, "external/usage"), "/"))
	case resource == "external/rebind" || strings.HasPrefix(resource, "external/rebind/"):
		a.externalRebindRoute(w, r, op, strings.TrimPrefix(strings.TrimPrefix(resource, "external/rebind"), "/"))
	case resource == "runtime/status":
		a.runtimeStatus(w, r, op)
	case resource == "runtime/artifact" || strings.HasPrefix(resource, "runtime/artifact/"):
		a.runtimeArtifact(w, r, op, resource)
	case resource == "runtime/operations" || strings.HasPrefix(resource, "runtime/operations/"):
		a.runtimeOperations(w, r, op, resource)
	case resource == "operation" && r.Method == http.MethodGet:
		writeFreshJSON(w, op)
	case strings.HasPrefix(resource, "cpamp/"):
		a.legacyResource(w, r, op, resource)
	case resource == "installation/runtime" && r.Method == http.MethodGet:
		a.installationRuntime(w, r, op)
	case resource == "installation/adoption" && r.Method == http.MethodGet:
		a.adoptionSummary(w, r, op)
	case resource == "strategy" && r.Method == http.MethodPost:
		a.executeStrategy(w, r, op)
	case resource == "complete" && r.Method == http.MethodPost:
		// An adoption's completion reads the migration summary the run is still
		// filling in, so the whole decision — the in-flight check, the conditions
		// and the CAS that sets `ProductComplete` — is taken under the same mutex a
		// run takes to start. A run therefore can never begin between the check
		// and the CAS, and a completion decided while one is running changes
		// nothing.
		if op.Strategy.Strategy == bootstrap.AdoptExisting {
			a.adoptComplete(w, r, op, record, continuation, freshAuth)
			return
		}
		if op.ProductComplete {
			// A replay is also the retry trigger for a completion whose usage
			// startup failed (for example while the Collector authority was
			// temporarily unavailable). The attempt is idempotent.
			a.startUsageCollection(op)
			writeFreshJSON(w, op)
			return
		}
		if op.Strategy.Strategy == bootstrap.ConnectExternal {
			// The safe-read result and encrypted Manager configuration were saved
			// atomically. Completing Manager setup does not activate model traffic
			// or take ownership of the external CPA lifecycle.
			if op.Strategy.Stage != "committed" || record.Connection == nil || a.protector == nil {
				w.WriteHeader(http.StatusConflict)
				return
			}
			if _, err := a.protector.UnprotectString(record.Connection.ManagementKey); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		} else if !a.managedInstallationReady(r.Context(), op) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		next, err := a.store.Update(r.Context(), op.Identity, op.Revision, func(record *freshstore.Record) error {
			if freshAuth {
				if err := a.authority.ValidateContinuation(*record, continuation); err != nil {
					return err
				}
			}
			if record.Operation.Strategy.Stage != "committed" {
				return freshstore.ErrConflict
			}
			record.Operation.ProductComplete = true
			return nil
		})
		if err != nil {
			w.WriteHeader(http.StatusConflict)
			return
		}
		if _, err := a.ensureManagedRuntimeDesiredRunning(r.Context(), next.Operation); err != nil {
			log.Printf("Fresh managed runtime desired state unavailable: %v", err)
		}
		a.pluginCapability.reprove(r.Context())
		// Completion is the moment the install-derived CPA authority exists, so
		// the same idempotent startup path as a resumed install runs now: it
		// backfills keys staged before completion and starts collection,
		// projection and identity reconciliation exactly once.
		a.startUsageCollection(next.Operation)
		writeFreshJSON(w, next.Operation)
	default:
		http.NotFound(w, r)
	}
}

func (a *freshApplication) fixedRuntime(w http.ResponseWriter, r *http.Request) {
	token, ok := freshbootstrap.SessionToken(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	op, err := a.authority.Authorize(r.Context(), token)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	a.installationRuntime(w, r, op)
}

// fixedAdoption serves the migration summary on the pre-cutover carrier. It
// authorizes with the installation session exactly as the runtime observation
// does: an adoption's progress belongs to the claim that committed it. The
// summary is reachable here only while the routing checkpoint is uncommitted —
// freshbootstrap.Handler answers 404 for every resource once it is committed,
// and fresh.Service.Authorize refuses a committed claim. After the cutover the
// same `installation/adoption` resource is served by canonicalAPI: to that
// session until the product completes, and to the Admin key afterwards.
func (a *freshApplication) fixedAdoption(w http.ResponseWriter, r *http.Request) {
	token, ok := freshbootstrap.SessionToken(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	op, err := a.authority.Authorize(r.Context(), token)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	a.adoptionSummary(w, r, op)
}

func (a *freshApplication) fixedStrategy(w http.ResponseWriter, r *http.Request) {
	token, ok := freshbootstrap.SessionToken(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	op, err := a.authority.Authorize(r.Context(), token)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	a.executeStrategy(w, r, op)
}

func (a *freshApplication) executeStrategy(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation) {
	var request struct {
		Identity         bootstrap.FreshIdentity `json:"identity"`
		ExpectedRevision uint64                  `json:"expectedRevision"`
		AttemptID        string                  `json:"attemptId,omitempty"`
		freshstrategy.Request
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil || request.Identity != op.Identity {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// A valid Fresh claim session is the whole trust anchor of an adoption: the
	// host operator who ran `initialize` alone holds this handoff, so an
	// authenticated strategy request is the operator's own intent and needs no
	// second Host permit. Only the source classes the product does not adopt stay
	// refused, and they are refused before the source GET, Runtime stage/start or
	// any Manager checkpoint mutation, so no refusal has a side effect.
	if request.Strategy == bootstrap.AdoptExisting && request.AdoptSourceClass != bootstrap.AdoptNetworkExisting {
		// A v7 or CPAMP-managed source is Upgrade's path. A missing class is
		// equally inadmissible: adoption never guesses which generation it reads.
		writeFreshStrategyError(w, &freshstrategy.Blocker{Code: "adoption_source_class_refused"})
		return
	}
	if request.ExpectedRevision != op.Revision {
		// A stale External CAS is a conflict, not an invalid user-supplied target.
		if request.Strategy == bootstrap.ConnectExternal {
			w.WriteHeader(http.StatusConflict)
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
		return
	}
	var externalBaseURL string
	if request.Strategy == bootstrap.ConnectExternal {
		if op.ProductComplete || (op.Strategy.Strategy != "" &&
			(op.Strategy.Strategy != bootstrap.ConnectExternal || op.Strategy.Stage != "validated")) {
			w.WriteHeader(http.StatusConflict)
			return
		}
		// Reuse the same compiled consumer qualification and static input rules
		// as Execute, without performing any CPA network request.
		var preflightErr error
		externalBaseURL, preflightErr = a.strategy.PreflightExternal(request.Request)
		if preflightErr != nil {
			writeFreshStrategyError(w, preflightErr)
			return
		}
		// Non-secret request identifier, never a credential or authorization.
		if !validFreshExternalAttemptID(request.AttemptID) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if a.externalPermit != nil && !a.externalPermit(op, request.ExpectedRevision, externalBaseURL, request.ManagementKey, request.AttemptID) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// CPA validation is read-only. Reserve no durable failure state before
		// it: a failed read is safe to retry. Only the final CAS may select the
		// connection. Also recover old validated-only checkpoints in place.
	}
	if request.Strategy == bootstrap.AdoptExisting || request.Strategy == bootstrap.NewManaged {
		record, err := a.store.Load(r.Context())
		if err != nil || record.Operation.Identity != op.Identity || record.Operation.Revision != op.Revision {
			w.WriteHeader(http.StatusConflict)
			return
		}
		request.Request.AdoptionCiphertext = record.AdoptionCiphertext
	}
	executionOp := op
	if request.Strategy == bootstrap.ConnectExternal && op.Strategy.Stage == "validated" {
		executionOp.Strategy = bootstrap.FreshStrategyCheckpoint{}
	}
	result, err := a.strategy.Execute(r.Context(), executionOp, request.Request)
	if err != nil {
		// A terminally failed Supervisor stage or start is the only evidence that
		// permits the next destination attempt. Retire it under the same revision
		// CAS before reporting the blocker, so a browser retry derives a new
		// operation ID instead of replaying the recorded failure forever.
		var retry *freshstrategy.DestinationRetry
		if errors.As(err, &retry) {
			guard := func(record freshstore.Record) error {
				if token, freshAuth := freshbootstrap.SessionToken(r); freshAuth {
					return a.validateInstallationSession(record, token)
				}
				return nil
			}
			if retireErr := a.retireDestinationAttempt(r.Context(), op, retry.Checkpoint, guard); retireErr != nil {
				w.WriteHeader(http.StatusConflict)
				return
			}
		}
		writeFreshStrategyError(w, err)
		return
	}
	if request.Strategy == bootstrap.ConnectExternal && a.externalPermit != nil && !a.externalPermit(op, request.ExpectedRevision, externalBaseURL, request.ManagementKey, request.AttemptID) {
		// A revoked/expired Host permit cannot complete a previously claimed attempt.
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if token, freshAuth := freshbootstrap.SessionToken(r); freshAuth {
		current, err := a.store.Load(r.Context())
		if err != nil || current.Operation.Identity != op.Identity || current.Operation.Revision != op.Revision || a.validateInstallationSession(current, token) != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	if op.Strategy.Stage == "committed" {
		// Reconcile only after the strategy adapter validates the requested
		// target and current server-owned qualification. Do not rewrite a
		// committed checkpoint, connection ciphertext or revision on replay.
		if op.Strategy.Strategy == bootstrap.AdoptExisting {
			// A committed adoption replay is the resume path of the migration:
			// the executor skips every item the record already settled, so a run
			// that stopped on a failed checkpoint, an unreachable source or an
			// unknown write continues from the durable prefix instead of repeating
			// it. Only the per-item summary moves, which is what the store's
			// monotone rule permits.
			//
			// The copy runs on its own worker, so this reply is not held by it: it
			// states that the migration is running and the record carries the
			// progress, exactly as the commit reply below does.
			if replay, err := a.store.Load(r.Context()); err == nil {
				token, _ := freshbootstrap.SessionToken(r)
				writeFreshJSON(w, adoptMigrationReply{FreshOperation: replay.Operation,
					MigrationState: a.runAdoptionMigration(r.Context(), replay, request.BaseURL, request.ManagementKey, token)})
				return
			}
		}
		writeFreshJSON(w, op)
		return
	}
	a.embeddedModeLock.Lock()
	defer a.embeddedModeLock.Unlock()
	current, err := a.store.Load(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if current.Operation.Identity != op.Identity ||
		current.Operation.Revision != op.Revision ||
		current.Operation.ProductComplete ||
		current.Operation.Strategy.Stage == "committed" {
		w.WriteHeader(http.StatusConflict)
		return
	}
	var finishExternalCommit func(committed bool)
	if request.Strategy == bootstrap.ConnectExternal {
		var fenceErr error
		finishExternalCommit, fenceErr = a.beginExternalCommit(r.Context())
		if fenceErr != nil {
			writeFreshStrategyError(w, fenceErr)
			return
		}
	}
	next, err := a.store.Update(r.Context(), op.Identity, op.Revision, func(record *freshstore.Record) error {
		if request.Strategy == bootstrap.ConnectExternal && a.externalPermit != nil && !a.externalPermit(record.Operation, request.ExpectedRevision, externalBaseURL, request.ManagementKey, request.AttemptID) {
			return errFreshExternalPermitRevoked
		}
		if token, freshAuth := freshbootstrap.SessionToken(r); freshAuth {
			if err := a.validateInstallationSession(*record, token); err != nil {
				return err
			}
		}
		record.Operation.Strategy = result.Checkpoint
		if result.AdoptionCiphertext != "" {
			record.AdoptionCiphertext = result.AdoptionCiphertext
		}
		if result.Connection != nil {
			record.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: result.Connection.BaseURL, ManagementKey: result.Connection.ManagementKeyCiphertext}
			// The plugin capability describes the connection that was saved, so a
			// new connection must not inherit the previous one's answer.
			a.pluginCapability.forget()
		}
		return nil
	})
	if err != nil {
		if finishExternalCommit != nil {
			finishExternalCommit(false)
		}
		if errors.Is(err, errFreshExternalPermitRevoked) {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusConflict)
		}
		return
	}
	if finishExternalCommit != nil {
		finishExternalCommit(true)
	}
	if result.InitialClientKey != "" {
		// The managed install just committed its destination, so its plugin answer
		// is proven against the new authority from the next read, not inherited from
		// whatever the pre-commit connection reported.
		a.pluginCapability.forget()
		// The call that commits the destination is the only reply that carries
		// the installation's Client Key. It is written once, in the same response
		// as the committed checkpoint, so the completion screen can show it; the
		// committed replay above never repeats it and no journal or log holds it.
		w.Header().Set("Cache-Control", "no-store")
		writeFreshJSON(w, struct {
			bootstrap.FreshOperation
			InitialClientKey string `json:"initialClientKey"`
		}{next.Operation, result.InitialClientKey})
		return
	}
	if next.Operation.Strategy.Strategy == bootstrap.AdoptExisting && next.Operation.Strategy.Stage == "committed" {
		// The destination is committed and its client keys verified, so the
		// bounded source inventory may now be copied into it. This is the hook
		// Step 4's completion conditions read: the per-item summary is durable,
		// and every outcome other than `migrated` is operator work rather than an
		// adoption failure.
		//
		// The copy runs on its own worker. This reply returns at once with the
		// state `running` and the committed revision it would have returned
		// anyway; the migration's own checkpoints advance the durable revision
		// afterwards, which is what the completion screen reads.
		token, _ := freshbootstrap.SessionToken(r)
		writeFreshJSON(w, adoptMigrationReply{FreshOperation: next.Operation,
			MigrationState: a.runAdoptionMigration(r.Context(), next, request.BaseURL, request.ManagementKey, token)})
		return
	}
	writeFreshJSON(w, next.Operation)
}

func (a *freshApplication) validateInstallationSession(record freshstore.Record, token string) error {
	if record.Operation.Routing.Committed {
		return a.authority.ValidateContinuation(record, token)
	}
	return a.authority.ValidateSession(record, token)
}

// retireDestinationAttempt commits the next managed destination attempt in the
// same Fresh CAS as the terminal failure that made it necessary. It advances the
// attempt by exactly one against the current revision, keeps every other
// checkpoint field, and never rewrites a checkpoint that already published its
// destination; a committed destination is not retryable by construction.
//
// Adoption retires attempts on the same terms, bounded by
// MaxAdoptionDestinationRetry: an adoption gets its original attempt plus exactly
// one retry, and the third terminal failure is reported without a retirement.
func (a *freshApplication) retireDestinationAttempt(ctx context.Context, op bootstrap.FreshOperation, checkpoint bootstrap.FreshStrategyCheckpoint, guard func(freshstore.Record) error) error {
	if checkpoint.Strategy != bootstrap.NewManaged && checkpoint.Strategy != bootstrap.AdoptExisting ||
		checkpoint.DestinationAttempt == 0 || checkpoint.DestinationConnectionIdentity != "" && checkpoint.Stage == "committed" {
		return freshstore.ErrConflict
	}
	if checkpoint.Strategy == bootstrap.AdoptExisting && checkpoint.DestinationAttempt > bootstrap.MaxAdoptionDestinationRetry {
		return freshstore.ErrConflict
	}
	_, err := a.store.Update(ctx, op.Identity, op.Revision, func(record *freshstore.Record) error {
		current := record.Operation.Strategy
		if current.Strategy != checkpoint.Strategy || current.DestinationConnectionIdentity != "" && current.Stage == "committed" || current.DestinationAttempt+1 != checkpoint.DestinationAttempt {
			return freshstore.ErrConflict
		}
		if guard != nil {
			if err := guard(*record); err != nil {
				return err
			}
		}
		record.Operation.Strategy = checkpoint
		return nil
	})
	return err
}

// adoptMigrationReply is the committed Adopt reply. It is the committed
// operation plus the state of the copy that runs beside it: the reply never
// waits for the copy, so it has to say what is true now.
type adoptMigrationReply struct {
	bootstrap.FreshOperation
	MigrationState string `json:"migrationState,omitempty"`
}

func writeFreshJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func freshDriver(cfg config.Config) freshrouting.Driver {
	driver, err := freshrouting.NewHTTPDriver(freshrouting.HTTPConfig{
		IngressControlURL: strings.TrimSpace(os.Getenv("CPAMP_INGRESS_CONTROL_URL")),
		PublicOrigin:      strings.TrimSpace(os.Getenv("CPAMP_PUBLIC_ORIGIN")),
	})
	if err != nil {
		return nil
	}
	return driver
}

func restoreFreshRouting(ctx context.Context, service *freshrouting.Service) {
	// Ingress is stateless. Re-deliver only the original persisted projection
	// after an independent Ingress restart; this never advances DB authority.
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = service.Restore(probe)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func freshMarkerPresent(database string) bool {
	info, err := os.Lstat(database + ".manager.lock")
	// An ordinary historical empty marker is not Fresh evidence. Any bounded
	// non-empty marker requires strict parsing by admission and fails closed.
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func serveExistingFresh(ctx context.Context, cfg config.Config, paths sqliterepo.InspectionPaths, h *startupHTTPHandler) (*fresh.Opened, error) {
	if err := validateFreshHostAuthority(cfg); err != nil {
		return nil, err
	}
	opened, err := fresh.Open(ctx, paths, fresh.OpenOptions{AdminKey: cfg.AdminKey, DataKey: cfg.DataKey})
	if err != nil {
		return nil, err
	}
	app := newFreshApplication(opened, cfg, freshDriver(cfg))
	// Workers started by an in-process completion must use the host lifetime,
	// not the request that triggered them.
	app.runCtx = ctx
	op := app.prepareCompletedStartup(ctx, opened.Record.Operation)
	h.SetFreshHandler(app)
	if app.runtime != nil && app.embeddedDesiredStateInner() != nil {
		go app.runFreshEmbeddedReconciler(ctx, runtimeservice.DefaultReconcileInterval)
	}
	if op.ProductComplete {
		app.startUsageCollection(op)
		if op.Strategy.Strategy == bootstrap.AdoptExisting {
			go app.awaitCompletedAdoptionBackfill(ctx)
		}
	}
	go restoreFreshRouting(ctx, app.routing)
	go app.runFreshUpdateChecks(ctx)
	return opened, nil
}

func validateFreshHostAuthority(cfg config.Config) error {
	if cfg.AdminAuthorityConfigured && cfg.AdminKey == "" {
		return fresh.ErrAuthority
	}
	return nil
}

func awaitFreshStore(ctx context.Context, cfg config.Config, paths sqliterepo.InspectionPaths, h *startupHTTPHandler) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !freshMarkerPresent(paths.Database) {
			continue
		}
		opened, err := serveExistingFresh(ctx, cfg, paths, h)
		if err != nil {
			continue
		}
		<-ctx.Done()
		_ = opened.Close()
		return
	}
}
