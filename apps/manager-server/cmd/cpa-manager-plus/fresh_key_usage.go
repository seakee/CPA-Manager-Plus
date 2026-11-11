package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	usageapp "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
)

// maxUsageWindowQuery bounds explicit usage window requests so one read cannot
// scan unbounded history.
const maxUsageWindowQueryMS = int64(31 * 24 * time.Hour / time.Millisecond)

// installedRuntimeIdentity is the runtime identity staged keys bind their
// source hash to. It comes from the committed installation checkpoint and is
// empty while no managed install is committed, which defers the binding to the
// startup backfill instead of guessing a runtime.
func (a *freshApplication) installedRuntimeIdentity(ctx context.Context) (string, error) {
	if a == nil || a.store == nil {
		return "", errors.New("Fresh store unavailable")
	}
	record, err := a.store.Load(ctx)
	if err != nil {
		return "", err
	}
	checkpoint := record.Operation.Strategy
	if !freshkeyapply.ManagerOwnedStrategy(checkpoint) || checkpoint.Stage != "committed" {
		return "", nil
	}
	return checkpoint.TargetIdentity, nil
}

// collectionPhase reports the live Collector state without exposing any CPA
// address, credential or error text to the browser.
func (a *freshApplication) collectionPhase() usageapp.CollectionPhase {
	if a == nil || a.legacyCollector == nil {
		return usageapp.PhaseStarting
	}
	switch a.legacyCollector.Status().Collector {
	case "running":
		return usageapp.PhaseRunning
	case "starting":
		return usageapp.PhaseStarting
	case "error":
		return usageapp.PhaseError
	default:
		return usageapp.PhaseStopped
	}
}

// usageMonitoringEnabled reports the administrator's stored request-monitoring
// switch. It is the authority the Collector start consults, so an installation
// whose switch is Off does not resume collecting after a restart. A missing
// document or a missing value is the On default a new installation completes
// with, which is why absence is not read as Off.
func (a *freshApplication) usageMonitoringEnabled(ctx context.Context) (bool, error) {
	if a == nil || a.runtimeSettings == nil {
		return true, nil
	}
	cfg, found, err := a.runtimeSettings.LoadManagerConfig(ctx)
	if err != nil {
		return false, err
	}
	if !found || cfg.Collector.Enabled == nil {
		return true, nil
	}
	return *cfg.Collector.Enabled, nil
}

// applyUsageMonitoringSwitch reconciles this process with the narrow switch the
// administrator just wrote. Off stops the Collector and drops the latched start,
// so the workspace stops claiming a stream the switch forbids; On re-arms
// through the one startup entry point without restarting an already-running
// Collector.
func (a *freshApplication) applyUsageMonitoringSwitch(ctx context.Context, op bootstrap.FreshOperation) {
	a.reconcileUsageMonitoring(ctx, op, false)
}

// applyUsageManagerConfig reconciles the host-bound Collector after a
// whole-document Manager Config PUT. Unlike the narrow switch PATCH, a full PUT
// may change queue, poll interval, batch size, TLS or CPA credentials while
// leaving collector.enabled true; the Collector restarts on a.runCtx only when
// its effective configuration or authority changed, and stops if the updated
// configuration no longer proves an authority.
func (a *freshApplication) applyUsageManagerConfig(ctx context.Context, op bootstrap.FreshOperation) {
	a.reconcileUsageMonitoring(ctx, op, true)
}

func (a *freshApplication) reconcileUsageMonitoring(ctx context.Context, op bootstrap.FreshOperation, reloadConfig bool) {
	if a == nil || a.usage == nil {
		return
	}
	reconcileCtx := a.runCtx
	if reconcileCtx == nil {
		reconcileCtx = context.WithoutCancel(ctx)
	}
	if a.runCtx != nil && a.usageProjection != nil {
		startFreshDatabaseMaintenance(a.runCtx, a.cfg.DBPath)
	}
	if hook := a.beforeUsageReconcileLock; hook != nil {
		hook()
	}
	a.usageStartMu.Lock()
	defer a.usageStartMu.Unlock()
	enabled, err := a.usageMonitoringEnabled(reconcileCtx)
	if err != nil {
		log.Printf("Fresh request monitoring switch unavailable: %v", err)
		a.stopUsageCollectionLocked(reconcileCtx)
		return
	}
	if !enabled {
		a.stopUsageCollectionLocked(reconcileCtx)
		a.setUsageStartReason(usageStartReasonMonitoringDisabled)
		return
	}
	if a.usageCollectorStarted && a.collectionPhase() != usageapp.PhaseStopped {
		if !reloadConfig || a.runningCollectorMatchesConfigLocked(reconcileCtx, op) {
			return
		}
	}
	a.stopUsageCollectionLocked(reconcileCtx)
	if a.runCtx == nil || a.usageProjection == nil {
		log.Printf("Fresh usage workers not started: no host lifetime")
		return
	}
	if a.attemptUsageStart(a.runCtx, op) {
		a.usageStarted = true
		a.cancelManagedUsageRuntimeWaiterLocked()
	}
}

func (a *freshApplication) runningCollectorMatchesConfigLocked(ctx context.Context, op bootstrap.FreshOperation) bool {
	if a.legacyCollector == nil {
		return false
	}
	activeCfg, active := a.legacyCollector.ActiveRuntimeConfig()
	if !active {
		return false
	}
	checkpoint := op.Strategy
	switch {
	case a.privateCollector != nil:
		desiredCfg, ok := a.privateCollectorDesiredConfig(ctx)
		return ok && activeCfg == desiredCfg
	case checkpoint.Strategy == bootstrap.ConnectExternal && a.externalSource != nil:
		install, err := a.externalSource(ctx)
		if err != nil {
			return false
		}
		desiredCfg, err := a.usageRuntimeConfig(ctx, install.BaseURL, install.ManagementKey)
		return err == nil && activeCfg == desiredCfg
	case freshkeyapply.ManagerOwnedStrategy(checkpoint) && a.managedSource != nil:
		install, err := a.managedSource(ctx)
		if err != nil {
			return false
		}
		desiredCfg, err := a.usageRuntimeConfig(ctx, install.BaseURL, install.ManagementKey)
		return err == nil && activeCfg == desiredCfg
	default:
		return false
	}
}

func (a *freshApplication) privateCollectorDesiredConfig(ctx context.Context) (collector.RuntimeConfig, bool) {
	if a.cfg.CPAUpstreamURL != "" && a.cfg.ManagementKey != "" {
		return collector.RuntimeConfig{
			CPAUpstreamURL: a.cfg.CPAUpstreamURL,
			ManagementKey:  a.cfg.ManagementKey,
			CollectorMode:  a.cfg.CollectorMode,
			Queue:          a.cfg.Queue,
			PopSide:        a.cfg.PopSide,
			BatchSize:      a.cfg.BatchSize,
			PollInterval:   a.cfg.PollInterval,
			TLSSkipVerify:  a.cfg.TLSSkipVerify,
		}, true
	}
	if a.runtimeSettings == nil {
		return collector.RuntimeConfig{}, false
	}
	managerCfg, ok, err := a.runtimeSettings.LoadManagerConfig(ctx)
	if err != nil || !ok || managerCfg.CPAConnection.CPABaseURL == "" || managerCfg.CPAConnection.ManagementKey == "" {
		return collector.RuntimeConfig{}, false
	}
	return collectorservice.RuntimeConfigFromManagerConfigWithFallback(managerCfg, a.cfg), true
}

// startUsageCollection arms the real usage chain of a completed installation:
// staged-key bindings (including keys created before completion), the canonical
// identity projection, the Collector reading the install-derived CPA authority,
// and the durable collection start instant. It runs on the host lifetime, never
// on the request that triggered it, so a client disconnect cannot abort a
// durable start. Attempts are serialized, and only a fully successful attempt is
// recorded: a failed start is retried by a completion replay or a later trigger.
// Nothing is claimed when the authority is unavailable: coverage then reports
// that collection was never proven.
func (a *freshApplication) startUsageCollection(op bootstrap.FreshOperation) {
	if a == nil || a.usageProjection == nil || a.usage == nil {
		return
	}
	// Long-running workers must outlive the request that triggered them and stop
	// with the host. The host installs that lifetime before serving this
	// application; without one, hosting them would leak past the database.
	runCtx := a.runCtx
	if runCtx == nil {
		log.Printf("Fresh usage workers not started: no host lifetime")
		return
	}
	// WAL maintenance is the other background owner of this installation's
	// database. The legacy startup creates it in main(), which the Fresh service
	// returns from earlier, so it starts here with the same host lifetime.
	startFreshDatabaseMaintenance(runCtx, a.cfg.DBPath)
	a.usageStartMu.Lock()
	defer a.usageStartMu.Unlock()
	if a.usageStarted {
		return
	}
	if a.attemptUsageStart(runCtx, op) {
		a.usageStarted = true
		a.cancelManagedUsageRuntimeWaiterLocked()
	}
}

// attemptUsageStart performs one startup attempt on the host lifetime. It
// reports success only when every durable step committed; serialization and the
// worker flag keep a retry from starting a second Collector or worker.
func (a *freshApplication) attemptUsageStart(ctx context.Context, op bootstrap.FreshOperation) bool {
	// Bind staged keys before the first event arrives so it is attributable,
	// and backfill keys that were staged before a committed target existed. No
	// event may be collected before its key is attributable, so a failed backfill
	// fails the whole attempt instead of starting a Collector that loses history.
	checkpoint := op.Strategy
	if freshkeyapply.ManagerOwnedStrategy(checkpoint) && checkpoint.Stage == "committed" && checkpoint.TargetIdentity != "" {
		if _, err := a.keyRepo.BindFreshKeySources(ctx, checkpoint.TargetIdentity, time.Now().UnixMilli()); err != nil {
			log.Printf("Fresh key usage bindings unavailable: %v", err)
			return false
		}
	}
	// The projection and identity workers maintain attribution independently of
	// the Collector authority, so they start once the keys are attributable.
	if !a.usageWorkersStarted {
		if a.startIdentityWorker != nil && checkpoint.Strategy != bootstrap.ConnectExternal {
			a.startIdentityWorker(ctx)
		}
		a.usageProjection.Start(ctx)
		a.usageWorkersStarted = true
	}
	// Starting the Collector is its own success-only step. A retry after a
	// durable marker failure only retries the marker: StartRuntime cancels the
	// running Collector, so repeating it would open a restart window that loses
	// whatever arrived in between.
	if !a.usageCollectorStarted {
		// The administrator's stored switch governs collection, and a restart is
		// exactly where it used to be ignored: this process starts a Collector for
		// every completed installation, so an installation switched off came back
		// collecting on the next start. An explicit Off therefore starts nothing
		// and records no collection window; WAL maintenance already ran above,
		// because database upkeep is not collection.
		enabled, err := a.usageMonitoringEnabled(ctx)
		if err != nil {
			// An unreadable switch proves neither state, so the Collector stays
			// closed and a later trigger retries the attempt.
			log.Printf("Fresh request monitoring switch unavailable: %v", err)
			return false
		}
		if !enabled {
			a.setUsageStartReason(usageStartReasonMonitoringDisabled)
			return false
		}
		started := false
		switch {
		case a.privateCollector != nil:
			// Only the explicit private profile owns a host-supplied Collector
			// authority.
			started = a.privateCollector(ctx)
		case checkpoint.Strategy == bootstrap.ConnectExternal && a.externalSource != nil:
			// An External installation collects from the user's own CPA through
			// the committed connection. A refused start proves nothing, so no
			// collection window is recorded for it.
			outcome := a.startExternalUsageCollector(ctx)
			a.setUsageStartReason(outcome.Reason)
			if !outcome.Started {
				return false
			}
			started = true
		case freshkeyapply.ManagerOwnedStrategy(checkpoint) && a.managedSource != nil && a.legacyCollector != nil:
			install, err := a.managedSource(ctx)
			if err != nil {
				// Managed and External installs have no other authority. Usage
				// stays unavailable instead of collecting from an unverified CPA.
				log.Printf("Fresh usage collection unavailable: %v", err)
				a.setUsageStartReason(usageStartReasonNoAuthority)
				return false
			}
			runtimeCfg, err := a.usageRuntimeConfig(ctx, install.BaseURL, install.ManagementKey)
			if err != nil {
				log.Printf("Fresh usage collection configuration unavailable: %v", err)
				a.setUsageStartReason(usageStartReasonCollectorFailed)
				return false
			}
			// After a restart, the embedded Supervisor begins Offline and Start
			// returns on spawn before CPA answers its readiness probe. Deferring
			// the Collector sample until the Supervisor observes Ready avoids a
			// doomed probe at startup and arms collection once auto-start settles.
			if a.runtime != nil {
				status, statusErr := a.observeInstallationRuntime(ctx)
				if statusErr != nil || status.State != model.RuntimeStateReady {
					a.setUsageStartReason(usageStartReasonCollectorFailed)
					a.armManagedUsageRuntimeWaiterLocked(ctx)
					return false
				}
			}
			a.activeExternalGrant.Store(activeCollectorGrant{})
			if err := a.legacyCollector.StartRuntime(ctx, runtimeCfg); err != nil {
				log.Printf("Fresh usage collection unavailable: %v", err)
				a.setUsageStartReason(usageStartReasonCollectorFailed)
				return false
			}
			// A live phase is not proof: the Collector reports "starting" before
			// it has authenticated anything. The first authenticated management
			// read is, so it gates the durable collection instant.
			if err := a.sampleUsageAuthority(ctx, install.BaseURL, install.ManagementKey); err != nil {
				_ = a.legacyCollector.Stop(ctx)
				log.Printf("Fresh usage collection sample unavailable: %v", err)
				a.setUsageStartReason(usageStartReasonCollectorFailed)
				return false
			}
			started = a.collectionPhase() != usageapp.PhaseStopped
			if !started {
				a.setUsageStartReason(usageStartReasonCollectorFailed)
			}
		default:
			// No Collector authority is configured for this strategy: nothing
			// starts, so nothing may be claimed either. Returning success here
			// would publish a coverage window that can never carry data.
			a.setUsageStartReason(usageStartReasonNoAuthority)
			return false
		}
		if !started {
			return false
		}
		a.usageCollectorStarted = true
		a.setUsageStartReason("")
	}
	if err := a.usage.MarkCollectionStart(ctx); err != nil {
		log.Printf("Fresh usage collection start unavailable: %v", err)
		return false
	}
	return true
}
func (a *freshApplication) cancelManagedUsageRuntimeWaiterLocked() {
	a.usageAwaitID++
	if a.usageAwaitCancel != nil {
		a.usageAwaitCancel()
		a.usageAwaitCancel = nil
	}
}

func (a *freshApplication) armManagedUsageRuntimeWaiterLocked(ctx context.Context) {
	if a.runtime == nil || a.store == nil || a.usageAwaitCancel != nil || ctx == nil || ctx.Err() != nil {
		return
	}
	waitCtx, cancel := context.WithCancel(ctx)
	a.usageAwaitID++
	waiterID := a.usageAwaitID
	a.usageAwaitCancel = cancel
	go a.awaitManagedRuntimeForUsage(waitCtx, cancel, waiterID)
}

func (a *freshApplication) claimManagedUsageRuntimeWaiter(waiterID uint64) bool {
	a.usageStartMu.Lock()
	defer a.usageStartMu.Unlock()
	if a.usageAwaitID != waiterID || a.usageStarted {
		return false
	}
	if a.usageAwaitCancel != nil {
		a.usageAwaitCancel()
		a.usageAwaitCancel = nil
	}
	a.usageAwaitID++
	return true
}

func (a *freshApplication) releaseManagedUsageRuntimeWaiter(waiterID uint64) {
	a.usageStartMu.Lock()
	defer a.usageStartMu.Unlock()
	if a.usageAwaitID == waiterID && a.usageAwaitCancel != nil {
		a.usageAwaitCancel()
		a.usageAwaitCancel = nil
	}
}

func (a *freshApplication) awaitManagedRuntimeForUsage(ctx context.Context, selfCancel context.CancelFunc, waiterID uint64) {
	defer func() {
		selfCancel()
		a.releaseManagedUsageRuntimeWaiter(waiterID)
	}()
	ticker := time.NewTicker(freshRuntimeReadinessInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		status, err := a.observeInstallationRuntime(ctx)
		if err != nil || status.State != model.RuntimeStateReady {
			continue
		}
		record, err := a.store.Load(ctx)
		if err != nil {
			continue
		}
		if !record.Operation.ProductComplete || !freshkeyapply.ValidManagerOwnedDestinationCheckpoint(record.Operation.Strategy) {
			return
		}
		if !a.claimManagedUsageRuntimeWaiter(waiterID) {
			return
		}
		a.startUsageCollection(record.Operation)
		return
	}
}

// usageWindow parses the optional bounded window of one key's usage read. The
// workspace never sends a window; explicit ranges exist for read-only
// verification and stay bounded.
func usageWindow(query map[string][]string) (fromMS, toMS int64, err error) {
	for name := range query {
		if name != "from_ms" && name != "to_ms" {
			return 0, 0, errors.New("unsupported usage window parameter")
		}
	}
	parse := func(name string) (int64, bool, error) {
		values, ok := query[name]
		if !ok {
			return 0, false, nil
		}
		if len(values) != 1 || values[0] == "" {
			return 0, false, errors.New("invalid usage window parameter")
		}
		value, convErr := strconv.ParseInt(values[0], 10, 64)
		if convErr != nil || value <= 0 {
			return 0, false, errors.New("invalid usage window parameter")
		}
		return value, true, nil
	}
	fromMS, hasFrom, err := parse("from_ms")
	if err != nil {
		return 0, 0, err
	}
	toMS, hasTo, err := parse("to_ms")
	if err != nil {
		return 0, 0, err
	}
	if hasFrom != hasTo {
		return 0, 0, errors.New("usage window requires both bounds")
	}
	if hasFrom && (toMS <= fromMS || toMS-fromMS > maxUsageWindowQueryMS) {
		return 0, 0, errors.New("invalid usage window range")
	}
	return fromMS, toMS, nil
}

// keyUsageSummary is the batched usage column of the Key list. Usage exists
// only for a completed installation: its Collector, projection and bindings all
// start after ProductComplete, so an earlier read is a state conflict, never a
// quiet zero.
func (a *freshApplication) keyUsageSummary(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation) {
	if !op.ProductComplete {
		w.WriteHeader(http.StatusConflict)
		return
	}
	if a.usage == nil || a.keys == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	ids, err := a.keys.IdentityIDs(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	summary, err := a.usage.Summary(r.Context(), ids)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	writeFreshJSON(w, summary)
}

// keyUsage is one key's usage section. Revoked keys keep their history because
// attribution is stored per Canonical Key, not per active binding.
func (a *freshApplication) keyUsage(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation, rawID string) {
	if !op.ProductComplete {
		w.WriteHeader(http.StatusConflict)
		return
	}
	if a.usage == nil || a.keys == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	key, err := a.keys.Detail(r.Context(), rawID)
	switch {
	case errors.Is(err, freshkeys.ErrRequest):
		w.WriteHeader(http.StatusBadRequest)
		return
	case errors.Is(err, freshkeys.ErrNotFound):
		w.WriteHeader(http.StatusNotFound)
		return
	case err != nil:
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	fromMS, toMS, err := usageWindow(r.URL.Query())
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	detail, err := a.usage.Detail(r.Context(), identity.APIKeyID(key.ID), fromMS, toMS)
	if errors.Is(err, usageapp.ErrWindow) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	writeFreshJSON(w, detail)
}
