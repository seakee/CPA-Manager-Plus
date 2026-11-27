package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	usageapp "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityusage"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
)

// maxUsageWindowQuery bounds explicit usage window requests so one read cannot
// scan unbounded history.
const maxUsageWindowQueryMS = int64(31 * 24 * time.Hour / time.Millisecond)

type runObservationWaiter struct {
	ch   chan struct{}
	done bool
	err  error
}

type runRetentionWaiter struct {
	ch       chan struct{}
	done     bool
	budgetMS int64
	err      error
}

func managedSourceScope(op bootstrap.FreshOperation) string {
	return string(op.Strategy.Strategy) + "|" + string(op.Identity.OperationID) + "|" + op.Strategy.TargetIdentity
}

func externalSourceScope(identity string) string {
	return "external|" + strings.TrimSpace(identity)
}

type warmStartUsageState struct {
	attempted          bool
	started            bool
	managedSourceErr   bool
	statisticsDisabled bool
	configErr          bool
	startErr           bool
	sampleErr          bool
	runID              uint64
}

type usageSettingsSnapshot struct {
	collectingAt         int64
	gatewayAdmittedAtMS  int64
	disabledGap          bool
	unreplayedGap        bool
	disableSeq           int64
	completionDisableSeq int64
	hasCompletionSeq     bool
}

// installedRuntimeIdentity is the runtime identity staged keys bind their
// source hash to. It derives exclusively from a committed, verified checkpoint
// (manager-owned runtime identity or persisted verified External identity) and
// returns empty while no verified target is committed.
func (a *freshApplication) installedRuntimeIdentity(ctx context.Context) (string, error) {
	if a == nil || a.store == nil {
		return "", errors.New("Fresh store unavailable")
	}
	record, err := a.store.Load(ctx)
	if err != nil {
		return "", err
	}
	checkpoint := record.Operation.Strategy
	if checkpoint.Stage != "committed" {
		return "", nil
	}
	if freshkeyapply.ManagerOwnedStrategy(checkpoint) {
		return checkpoint.TargetIdentity, nil
	}
	if checkpoint.Strategy == bootstrap.ConnectExternal {
		if a.protector == nil {
			return "", freshkeyapply.ErrUnavailable
		}
		install, err := externalInstallFromRecord(record, a.protector)
		if err != nil {
			return "", err
		}
		return install.Identity, nil
	}
	return "", nil
}

func (a *freshApplication) rawCollectionPhase() usageapp.CollectionPhase {
	if a == nil || a.legacyCollector == nil {
		return usageapp.PhaseStarting
	}
	return collectionPhaseFor(a.legacyCollector.Status().Collector)
}

// collectionPhase reports the live Collector state without exposing any CPA
// address, credential or error text to the browser.
func (a *freshApplication) collectionPhase() usageapp.CollectionPhase {
	if a == nil {
		return usageapp.PhaseStarting
	}
	if a.hasArmedManagedUsageRuntimeWaiter() {
		return usageapp.PhaseAwaitingRuntime
	}
	phase := a.rawCollectionPhase()
	if a.usageStartupPending.Load() && phase != usageapp.PhaseError {
		return usageapp.PhaseAwaitingRuntime
	}
	return phase
}

func (a *freshApplication) hasArmedManagedUsageRuntimeWaiter() bool {
	return a != nil && a.usageAwaitArmed.Load()
}

func collectionPhaseFor(status string) usageapp.CollectionPhase {
	switch status {
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

// isAdoptedPreBindingProven reports whether adopted keys in this store belong
// to a completed Fresh AdoptExisting destination created at adoption time
// (rather than an External connection, an incomplete staged adoption, or a
// 1.x -> v2 Upgrade).
func (a *freshApplication) isAdoptedPreBindingProven(ctx context.Context) bool {
	op, err := a.current(ctx)
	if err != nil {
		return false
	}
	return op.ProductComplete &&
		freshkeyapply.ManagerOwnedStrategy(op.Strategy) &&
		op.Strategy.Strategy == bootstrap.AdoptExisting &&
		op.Strategy.Stage == "committed"
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

func (a *freshApplication) readUsageSettingsSnapshot(ctx context.Context) (usageSettingsSnapshot, error) {
	if a == nil || a.usageDB == nil {
		return usageSettingsSnapshot{}, nil
	}
	rows, err := a.usageDB.QueryContext(ctx,
		`select key, value from settings where key in (?, ?, ?, ?, ?, ?)`,
		sqliterepo.FreshUsageCollectionStartedAtMSKey,
		sqliterepo.FreshGatewayAdmittedAtMSKey,
		sqliterepo.FreshUsageMonitoringDisabledGapKey,
		sqliterepo.FreshUsageUnreplayedGapKey,
		sqliterepo.FreshUsageMonitoringDisableSeqKey,
		sqliterepo.FreshUsageCompletionDisableSeqKey,
	)
	if err != nil {
		return usageSettingsSnapshot{}, err
	}
	defer rows.Close()
	var snap usageSettingsSnapshot
	for rows.Next() {
		var k, v string
		if scanErr := rows.Scan(&k, &v); scanErr != nil {
			return usageSettingsSnapshot{}, scanErr
		}
		trimmed := strings.TrimSpace(v)
		switch k {
		case sqliterepo.FreshUsageCollectionStartedAtMSKey:
			if parsed, parseErr := strconv.ParseInt(trimmed, 10, 64); parseErr == nil && parsed > 0 {
				snap.collectingAt = parsed
			}
		case sqliterepo.FreshGatewayAdmittedAtMSKey:
			if parsed, parseErr := strconv.ParseInt(trimmed, 10, 64); parseErr == nil && parsed > 0 {
				snap.gatewayAdmittedAtMS = parsed
			}
		case sqliterepo.FreshUsageMonitoringDisabledGapKey:
			snap.disabledGap = trimmed == "1"
		case sqliterepo.FreshUsageUnreplayedGapKey:
			snap.unreplayedGap = trimmed == "1"
		case sqliterepo.FreshUsageMonitoringDisableSeqKey:
			parsed, parseErr := strconv.ParseInt(trimmed, 10, 64)
			if parseErr != nil || parsed < 0 {
				return usageSettingsSnapshot{}, errors.New("corrupted usage monitoring disable sequence")
			}
			snap.disableSeq = parsed
		case sqliterepo.FreshUsageCompletionDisableSeqKey:
			if parsed, parseErr := strconv.ParseInt(trimmed, 10, 64); parseErr == nil {
				snap.completionDisableSeq = parsed
				snap.hasCompletionSeq = true
			}
		}
	}
	return snap, rows.Err()
}

func (a *freshApplication) readUsageDisableSeq(ctx context.Context) (int64, bool) {
	if a == nil || a.usageDB == nil {
		return 0, false
	}
	var raw string
	err := a.usageDB.QueryRowContext(ctx,
		`select value from settings where key = ?`,
		sqliterepo.FreshUsageMonitoringDisableSeqKey,
	).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, true
		}
		return 0, false
	}
	parsed, parseErr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if parseErr != nil || parsed < 0 {
		return 0, false
	}
	return parsed, true
}

func (a *freshApplication) ensureUsageMonitoringDisabledGap(ctx context.Context) {
	if a == nil || a.usage == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	writeCtx := context.WithoutCancel(ctx)
	hasGap, err := a.usage.MonitoringDisabledGap(writeCtx)
	if err == nil && hasGap {
		return
	}
	if err := a.usage.RecordMonitoringDisabledGap(writeCtx); err != nil {
		log.Printf("Fresh monitoring disabled gap write unavailable: %v", err)
	}
}

func (a *freshApplication) hasUsageMonitoringDisabledGapErr(ctx context.Context) (bool, error) {
	if a == nil || a.usage == nil {
		return false, nil
	}
	return a.usage.MonitoringDisabledGap(ctx)
}

func (a *freshApplication) registerRunObservationWaiterLocked(runID uint64) *runObservationWaiter {
	a.obsMu.Lock()
	defer a.obsMu.Unlock()
	if a.runObsWaiters == nil {
		a.runObsWaiters = make(map[uint64]*runObservationWaiter)
	}
	for id := range a.runObsWaiters {
		if id+4 < runID {
			delete(a.runObsWaiters, id)
		}
	}
	waiter := &runObservationWaiter{ch: make(chan struct{})}
	a.runObsWaiters[runID] = waiter
	return waiter
}

func (a *freshApplication) clearRunObservationWaiterLocked(runID uint64) {
	a.obsMu.Lock()
	defer a.obsMu.Unlock()
	if w := a.runObsWaiters[runID]; w != nil {
		if !w.done {
			w.done = true
			w.err = context.Canceled
			close(w.ch)
		}
		delete(a.runObsWaiters, runID)
	}
}

func (a *freshApplication) failRunObservationWaiter(runID uint64, err error) {
	if a == nil || err == nil {
		return
	}
	a.obsMu.Lock()
	defer a.obsMu.Unlock()
	if a.legacyCollector != nil && runID != 0 && a.legacyCollector.StartCount() != runID {
		return
	}
	if w := a.runObsWaiters[runID]; w != nil && !w.done {
		w.done = true
		w.err = err
		close(w.ch)
	}
}

func (a *freshApplication) registerRunRetentionPendingLocked(runID uint64) {
	if a == nil || runID == 0 {
		return
	}
	a.obsMu.Lock()
	defer a.obsMu.Unlock()
	if a.runRetentions == nil {
		a.runRetentions = make(map[uint64]*runRetentionWaiter)
	}
	for id := range a.runRetentions {
		if id+4 < runID {
			delete(a.runRetentions, id)
		}
	}
	a.runRetentions[runID] = &runRetentionWaiter{ch: make(chan struct{})}
}

func (a *freshApplication) resolveRunRetention(runID uint64, budgetMS int64) {
	if a == nil || runID == 0 {
		return
	}
	if budgetMS < 0 {
		budgetMS = 0
	}
	a.obsMu.Lock()
	defer a.obsMu.Unlock()
	if a.runRetentions == nil {
		a.runRetentions = make(map[uint64]*runRetentionWaiter)
	}
	for id := range a.runRetentions {
		if id+4 < runID {
			delete(a.runRetentions, id)
		}
	}
	w := a.runRetentions[runID]
	if w == nil {
		ch := make(chan struct{})
		close(ch)
		a.runRetentions[runID] = &runRetentionWaiter{ch: ch, done: true, budgetMS: budgetMS}
		return
	}
	if !w.done {
		w.done = true
		w.budgetMS = budgetMS
		close(w.ch)
	}
}

func (a *freshApplication) failRunRetention(runID uint64, err error) {
	if a == nil || runID == 0 {
		return
	}
	if err == nil {
		err = context.Canceled
	}
	a.obsMu.Lock()
	defer a.obsMu.Unlock()
	if w := a.runRetentions[runID]; w != nil && !w.done {
		w.done = true
		w.err = err
		close(w.ch)
	}
}

func (a *freshApplication) awaitRunRetention(ctx context.Context, runID uint64) (budgetMS int64, verified bool, err error) {
	if a == nil || runID == 0 {
		return 0, false, nil
	}
	a.obsMu.Lock()
	w := a.runRetentions[runID]
	if w == nil {
		a.obsMu.Unlock()
		return 0, false, nil
	}
	if w.done {
		budgetMS, err = w.budgetMS, w.err
		a.obsMu.Unlock()
		return budgetMS, err == nil, err
	}
	a.obsMu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-w.ch:
		a.obsMu.Lock()
		budgetMS, err = w.budgetMS, w.err
		a.obsMu.Unlock()
		return budgetMS, err == nil, err
	case <-ctx.Done():
		return 0, false, ctx.Err()
	}
}

func (a *freshApplication) resetObservationStateLocked(err error) {
	a.obsMu.Lock()
	defer a.obsMu.Unlock()
	a.warmStartPending = false
	a.pendingWarmObs = nil
	a.lastObserved = collector.ObservationEvent{}
	for _, w := range a.runObsWaiters {
		if w != nil && !w.done {
			w.done = true
			w.err = err
			close(w.ch)
		}
	}
	a.runObsWaiters = nil
	for _, w := range a.runRetentions {
		if w != nil && !w.done {
			w.done = true
			w.err = err
			close(w.ch)
		}
	}
	a.runRetentions = nil
}

func (a *freshApplication) waitForRunObservation(ctx context.Context, waiter *runObservationWaiter, timeout time.Duration) (bool, error) {
	if waiter == nil {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		select {
		case <-waiter.ch:
			a.obsMu.Lock()
			err := waiter.err
			a.obsMu.Unlock()
			return true, err
		default:
			return false, nil
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-waiter.ch:
		a.obsMu.Lock()
		err := waiter.err
		a.obsMu.Unlock()
		return true, err
	case <-ctx.Done():
		return false, ctx.Err()
	case <-timer.C:
		return false, nil
	}
}

func (a *freshApplication) observedRunStillActive(ev collector.ObservationEvent) func() bool {
	return func() bool {
		if a == nil || a.legacyCollector == nil || ev.RunID == 0 {
			return true
		}
		if !ev.InitialRunObserve && !ev.ForceGapAdvance {
			return a.legacyCollector.StartCount() == ev.RunID
		}
		return a.legacyCollector.ActiveRun(ev.RunID)
	}
}

func (a *freshApplication) recordObservation(ctx context.Context, ev collector.ObservationEvent) error {
	if a == nil || a.usage == nil {
		return nil
	}
	return a.usage.RecordObservedCollectionStart(ctx, ports.ObservedCollectionStart{
		ObservedAtMS:       ev.ObservedAtMS,
		RetainedQueue:      ev.RetainedQueue,
		Transport:          ev.Transport,
		SourceID:           ev.SourceID,
		RetentionBudgetMS:  ev.RetentionBudgetMS,
		ObservedDisableSeq: ev.PreObserveSeq,
		HasDisableSeq:      ev.PreObserveSeqSet,
		InitialRunObserve:  ev.InitialRunObserve,
		ForceGapAdvance:    ev.ForceGapAdvance,
		StillActive:        a.observedRunStillActive(ev),
	})
}

func (a *freshApplication) handleCollectorObserved(ctx context.Context, ev collector.ObservationEvent) error {
	if a == nil || (ctx != nil && ctx.Err() != nil) {
		return ports.ErrStaleObservation
	}
	if ev.RunID != 0 {
		waitCtx := ctx
		if waitCtx == nil {
			waitCtx = a.runCtx
		}
		if waitCtx == nil {
			waitCtx = context.Background()
		}
		budgetMS, verified, waitErr := a.awaitRunRetention(waitCtx, ev.RunID)
		if waitErr != nil {
			return ports.ErrStaleObservation
		}
		if verified {
			ev.RetentionBudgetMS = budgetMS
			ev.RetentionBudgetVerified = true
			if (ev.HadPollError || ev.OutageMS > 0) && (budgetMS <= 0 || ev.OutageMS > budgetMS) {
				ev.ForceGapAdvance = true
			}
		} else if !ev.RetentionBudgetVerified {
			ev.RetentionBudgetMS = 0
		}
	}
	a.obsMu.Lock()
	if a.legacyCollector != nil && ev.RunID != 0 && a.legacyCollector.StartCount() != ev.RunID {
		a.obsMu.Unlock()
		return ports.ErrStaleObservation
	}
	a.lastObserved = ev
	if a.warmStartPending {
		if ev.ForceGapAdvance {
			a.pendingWarmObs = nil
			if w := a.runObsWaiters[ev.RunID]; w != nil && !w.done {
				w.done = true
				w.err = ports.ErrStaleObservation
				close(w.ch)
			}
			a.obsMu.Unlock()
			return ports.ErrStaleObservation
		}
		if a.pendingWarmObs == nil || ev.InitialRunObserve {
			copyEv := ev
			a.pendingWarmObs = &copyEv
		}
		if w := a.runObsWaiters[ev.RunID]; w != nil && !w.done {
			w.done = true
			w.err = nil
			close(w.ch)
		}
		a.obsMu.Unlock()
		return nil
	}
	a.obsMu.Unlock()

	if (ctx != nil && ctx.Err() != nil) || (a.legacyCollector != nil && ev.RunID != 0 && a.legacyCollector.StartCount() != ev.RunID) {
		return ports.ErrStaleObservation
	}
	if ev.InitialRunObserve || ev.ForceGapAdvance {
		if hook := a.beforeUsageMarkerCommit; hook != nil {
			hook()
		}
		if (ctx != nil && ctx.Err() != nil) || (a.legacyCollector != nil && ev.RunID != 0 && a.legacyCollector.StartCount() != ev.RunID) {
			return ports.ErrStaleObservation
		}
	}
	commitCtx := a.runCtx
	if commitCtx == nil {
		commitCtx = context.WithoutCancel(ctx)
	}
	commitErr := a.recordObservation(commitCtx, ev)
	if (ctx == nil || ctx.Err() == nil) && (a.legacyCollector == nil || ev.RunID == 0 || a.legacyCollector.StartCount() == ev.RunID) {
		if !errors.Is(commitErr, ports.ErrStaleObservation) || ev.InitialRunObserve || ev.ForceGapAdvance {
			a.usageStartupPending.Store(false)
		}
	}
	if ev.InitialRunObserve || ev.ForceGapAdvance {
		a.obsMu.Lock()
		if w := a.runObsWaiters[ev.RunID]; w != nil && !w.done {
			w.done = true
			w.err = commitErr
			close(w.ch)
		}
		a.obsMu.Unlock()
	}
	return commitErr
}

func (a *freshApplication) handleCollectorUnreplayedDisconnect(ctx context.Context, runID uint64, _ string) {
	if a == nil || (ctx != nil && ctx.Err() != nil) {
		return
	}
	a.obsMu.Lock()
	if a.legacyCollector != nil && runID != 0 && a.legacyCollector.StartCount() != runID {
		a.obsMu.Unlock()
		return
	}
	if a.warmStartPending {
		a.pendingWarmObs = nil
		a.obsMu.Unlock()
		return
	}
	a.obsMu.Unlock()
	if a.usage == nil {
		return
	}
	recordCtx := a.runCtx
	if recordCtx == nil {
		recordCtx = context.WithoutCancel(ctx)
	}
	if enabled, err := a.usageMonitoringEnabled(recordCtx); err != nil || !enabled {
		return
	}
	if a.legacyCollector != nil && runID != 0 && a.legacyCollector.StartCount() != runID {
		return
	}
	if err := a.usage.RecordUnreplayedGap(recordCtx); err != nil {
		log.Printf("Fresh usage unreplayed gap write unavailable: %v", err)
	}
}

func (a *freshApplication) handleCollectorFirstPollError(ctx context.Context, runID uint64, _ string, err error) {
	if a == nil || err == nil || (ctx != nil && ctx.Err() != nil) {
		return
	}
	a.obsMu.Lock()
	if a.legacyCollector != nil && runID != 0 && a.legacyCollector.StartCount() != runID {
		a.obsMu.Unlock()
		return
	}
	if a.warmStartPending {
		a.pendingWarmObs = nil
	}
	if w := a.runObsWaiters[runID]; w != nil && !w.done {
		w.done = true
		w.err = err
		close(w.ch)
	}
	a.obsMu.Unlock()
	if a.legacyCollector == nil || runID == 0 || a.legacyCollector.StartCount() == runID {
		a.usageStartupPending.Store(false)
	}
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
	if hook := a.beforeUsageReconcileLock; hook != nil {
		hook()
	}
	a.usageStartMu.Lock()
	locked := true
	defer func() {
		if locked {
			a.usageStartMu.Unlock()
		}
	}()
	if a.runCtx != nil && a.usageProjection != nil && !a.isClosing() {
		a.startDatabaseMaintenance(a.lifecycleContext(a.runCtx))
	}
	enabled, err := a.usageMonitoringEnabled(reconcileCtx)
	if err != nil {
		log.Printf("Fresh request monitoring switch unavailable: %v", err)
		waitStop := a.stopCurrentUsageCollectionLocked(reconcileCtx)
		locked = false
		a.usageStartMu.Unlock()
		if waitStop != nil {
			_ = waitStop(reconcileCtx)
		}
		return
	}
	if !enabled {
		a.ensureUsageMonitoringDisabledGap(reconcileCtx)
		waitStop := a.stopCurrentUsageCollectionLocked(reconcileCtx)
		a.setUsageStartReason(usageStartReasonMonitoringDisabled)
		locked = false
		a.usageStartMu.Unlock()
		if waitStop != nil {
			_ = waitStop(reconcileCtx)
		}
		return
	}
	snap, snapErr := a.readUsageSettingsSnapshot(reconcileCtx)
	if a.usageCollectorStarted && a.rawCollectionPhase() != usageapp.PhaseStopped &&
		snapErr == nil && !snap.disabledGap && !snap.unreplayedGap && snap.disableSeq == a.lastStartedDisableSeq {
		if !reloadConfig || a.runningCollectorMatchesConfigLocked(reconcileCtx, op) {
			return
		}
	}
	if waitStop := a.stopCurrentUsageCollectionLocked(reconcileCtx); waitStop != nil {
		locked = false
		a.usageStartMu.Unlock()
		_ = waitStop(reconcileCtx)
		a.usageStartMu.Lock()
		locked = true
		if stillEnabled, eErr := a.usageMonitoringEnabled(reconcileCtx); eErr != nil || !stillEnabled {
			return
		}
		if a.usageCollectorStarted {
			return
		}
	}
	if a.runCtx == nil || a.usageProjection == nil || a.isClosing() {
		log.Printf("Fresh usage workers not started: no host lifetime")
		return
	}
	if a.attemptUsageStart(a.runCtx, op, false) {
		a.usageStarted = true
		a.cancelManagedUsageRuntimeWaiterLocked()
	}
}

func (a *freshApplication) stopCurrentUsageCollectionLocked(ctx context.Context) func(context.Context) error {
	var waitStop func(context.Context) error
	if a.legacyCollector != nil {
		if runID := a.legacyCollector.StartCount(); runID != 0 {
			waitStop = a.legacyCollector.CancelRun(runID)
		}
	}
	a.stopUsageCollectionLocked(ctx)
	return waitStop
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
	case isCommittedExternalStrategy(checkpoint) && a.externalSource != nil:
		install, err := a.externalSource(ctx)
		if err != nil {
			return false
		}
		desiredCfg, err := a.usageRuntimeConfig(ctx, install.BaseURL, install.ManagementKey)
		if err != nil {
			return false
		}
		desiredCfg.SourceScope = externalSourceScope(install.Identity)
		return activeCfg == desiredCfg
	case freshkeyapply.ManagerOwnedStrategy(checkpoint) && a.managedSource != nil:
		install, err := a.managedSource(ctx)
		if err != nil {
			return false
		}
		desiredCfg, err := a.usageRuntimeConfig(ctx, install.BaseURL, install.ManagementKey)
		if err != nil {
			return false
		}
		desiredCfg.SourceScope = managedSourceScope(op)
		return activeCfg == desiredCfg
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

func (a *freshApplication) warmStartUsageCollectorBeforeCompletion(ctx context.Context, op bootstrap.FreshOperation) warmStartUsageState {
	if a == nil || a.usageProjection == nil || a.usage == nil || a.runCtx == nil || a.isClosing() {
		return warmStartUsageState{}
	}
	if a.privateCollector != nil || !freshkeyapply.ManagerOwnedStrategy(op.Strategy) || a.managedSource == nil || a.legacyCollector == nil {
		return warmStartUsageState{}
	}

	var waiter *runObservationWaiter
	var expectedRunID uint64

	a.usageStartMu.Lock()
	if a.isClosing() || a.usageCollectorStarted || a.usageStarted {
		a.usageStartMu.Unlock()
		return warmStartUsageState{}
	}
	hasGap, gapErr := a.hasUsageMonitoringDisabledGapErr(a.runCtx)
	if gapErr != nil || hasGap {
		a.usageStartMu.Unlock()
		return warmStartUsageState{}
	}
	if a.runtime != nil {
		status, statusErr := a.observeInstallationRuntime(a.runCtx)
		if statusErr != nil || status.State != model.RuntimeStateReady {
			a.usageStartMu.Unlock()
			return warmStartUsageState{}
		}
	}
	install, err := a.managedSource(a.runCtx)
	if err != nil {
		a.usageStartMu.Unlock()
		return warmStartUsageState{attempted: true, managedSourceErr: true}
	}
	runtimeCfg, err := a.usageRuntimeConfig(a.runCtx, install.BaseURL, install.ManagementKey)
	if err != nil {
		a.usageStartMu.Unlock()
		return warmStartUsageState{attempted: true, configErr: true}
	}
	runtimeCfg.SourceScope = managedSourceScope(op)
	a.activeExternalGrant.Store(activeCollectorGrant{})
	expectedRunID = a.legacyCollector.StartCount() + 1
	a.obsMu.Lock()
	a.warmStartPending = true
	a.pendingWarmObs = nil
	a.obsMu.Unlock()
	waiter = a.registerRunObservationWaiterLocked(expectedRunID)
	a.registerRunRetentionPendingLocked(expectedRunID)
	a.usageStartupPending.Store(true)

	if err := a.legacyCollector.StartRuntime(a.lifecycleContext(a.runCtx), runtimeCfg); err != nil {
		a.failRunRetention(expectedRunID, err)
		a.usageStartupPending.Store(false)
		a.resetObservationStateLocked(err)
		a.usageStartMu.Unlock()
		return warmStartUsageState{attempted: true, startErr: true}
	}
	usageCfg, err := a.sampleUsageAuthority(a.runCtx, install.BaseURL, install.ManagementKey)
	if err != nil {
		a.failRunRetention(expectedRunID, err)
		waitStop := a.legacyCollector.CancelRun(expectedRunID)
		a.usageStartupPending.Store(false)
		a.resetObservationStateLocked(err)
		a.usageStartMu.Unlock()
		_ = waitStop(a.runCtx)
		return warmStartUsageState{attempted: true, sampleErr: true}
	}
	if usageCfg.HasUsageStatisticsEnabled && !usageCfg.UsageStatisticsEnabled {
		if a.usage != nil {
			_ = a.usage.RecordUnreplayedGap(a.runCtx)
		}
		statsErr := errors.New(usageStartReasonStatisticsDisabled)
		a.failRunRetention(expectedRunID, statsErr)
		waitStop := a.legacyCollector.CancelRun(expectedRunID)
		a.usageStartupPending.Store(false)
		a.resetObservationStateLocked(statsErr)
		a.setUsageStartReason(usageStartReasonStatisticsDisabled)
		a.usageStartMu.Unlock()
		_ = waitStop(a.runCtx)
		return warmStartUsageState{attempted: true, statisticsDisabled: true}
	}
	if a.rawCollectionPhase() == usageapp.PhaseStopped {
		a.failRunRetention(expectedRunID, errors.New("collector stopped"))
		waitStop := a.legacyCollector.CancelRun(expectedRunID)
		a.usageStartupPending.Store(false)
		a.resetObservationStateLocked(errors.New("collector stopped"))
		a.usageStartMu.Unlock()
		_ = waitStop(a.runCtx)
		return warmStartUsageState{attempted: true, startErr: true}
	}
	budgetMS := collector.VerifiedRetentionBudgetMS(usageCfg)
	a.legacyCollector.SetRunRetentionBudget(expectedRunID, budgetMS)
	a.resolveRunRetention(expectedRunID, budgetMS)
	a.usageCollectorStarted = true
	a.setUsageStartReason("")
	a.usageStartMu.Unlock()
	_, _ = a.waitForRunObservation(ctx, waiter, 500*time.Millisecond)
	return warmStartUsageState{
		attempted: true,
		started:   true,
		runID:     expectedRunID,
	}
}

func (a *freshApplication) rollbackWarmStartUsageCollector(ctx context.Context, warmState warmStartUsageState) {
	if a == nil || !warmState.started {
		return
	}
	a.usageStartMu.Lock()
	if a.legacyCollector != nil && warmState.runID != 0 && a.legacyCollector.StartCount() != warmState.runID {
		a.usageStartMu.Unlock()
		return
	}
	var waitStop func(context.Context) error
	if a.legacyCollector != nil {
		if warmState.runID != 0 {
			waitStop = a.legacyCollector.CancelRun(warmState.runID)
		} else {
			waitStop = a.legacyCollector.CancelRuntime()
		}
	}
	a.stopUsageCollectionLocked(context.WithoutCancel(ctx))
	a.usageStartMu.Unlock()
	if waitStop != nil {
		_ = waitStop(context.WithoutCancel(ctx))
	}
}

func (a *freshApplication) finishCompletionUsageCollection(_ context.Context, op bootstrap.FreshOperation, warmState warmStartUsageState) {
	a.startUsageCollectionWithWarmState(op, warmState)
}

// startUsageCollection arms the real usage chain of a completed installation:
// staged-key bindings (including keys created before completion), the canonical
// identity projection, the Collector reading the install-derived CPA authority,
// and the durable collection start instant. It runs on the host lifetime, never
// on the request that triggered it, so a client disconnect cannot abort a
// durable start. Attempts are serialized, and only a fully successful attempt is
// recorded: a failed start is retried by a completion replay or a later trigger.
func (a *freshApplication) startUsageCollection(op bootstrap.FreshOperation) {
	a.startUsageCollectionWithWarmState(op, warmStartUsageState{})
}

func (a *freshApplication) startUsageCollectionWithWarmState(op bootstrap.FreshOperation, warmState warmStartUsageState) {
	if a == nil || a.usageProjection == nil || a.usage == nil {
		return
	}
	// Long-running workers must outlive the request that triggered them and stop
	// with the host. The host installs that lifetime before serving this
	// application; without one, hosting them would leak past the database.
	runCtx := a.runCtx
	if runCtx == nil || a.isClosing() {
		log.Printf("Fresh usage workers not started: no host lifetime")
		return
	}
	if hook := a.beforeUsageStartLock; hook != nil {
		hook()
	}
	a.usageStartMu.Lock()
	defer a.usageStartMu.Unlock()
	if a.isClosing() {
		a.obsMu.Lock()
		a.warmStartPending = false
		a.pendingWarmObs = nil
		a.obsMu.Unlock()
		return
	}
	// WAL maintenance is the other background owner of this installation's
	// database. The legacy startup creates it in main(), which the Fresh service
	// returns from earlier, so it starts here with the same host lifetime.
	a.startDatabaseMaintenance(a.lifecycleContext(runCtx))
	if a.usageStarted {
		a.obsMu.Lock()
		a.warmStartPending = false
		a.pendingWarmObs = nil
		a.obsMu.Unlock()
		return
	}
	if a.attemptUsageStartWithWarmState(runCtx, op, false, warmState) {
		a.usageStarted = true
		a.cancelManagedUsageRuntimeWaiterLocked()
	}
}

func (a *freshApplication) attemptUsageStart(ctx context.Context, op bootstrap.FreshOperation, fromWaiter bool) bool {
	return a.attemptUsageStartWithWarmState(ctx, op, fromWaiter, warmStartUsageState{})
}

// attemptUsageStartWithWarmState performs one startup attempt on the host
// lifetime. It reports success only when every durable step committed;
// serialization and the worker flag keep a retry from starting a second
// Collector or worker.
func (a *freshApplication) attemptUsageStartWithWarmState(ctx context.Context, op bootstrap.FreshOperation, fromWaiter bool, warmState warmStartUsageState) bool {
	// Bind staged keys before attribution reads run, and backfill keys that were
	// staged before a committed target existed. No event may be projected before
	// its key is attributable, so a failed backfill fails the whole attempt.
	checkpoint := op.Strategy
	var bound int64
	if freshkeyapply.ManagerOwnedStrategy(checkpoint) && checkpoint.Stage == "committed" && checkpoint.TargetIdentity != "" {
		var err error
		bound, err = a.keyRepo.BindFreshKeySources(ctx, checkpoint.TargetIdentity, time.Now().UnixMilli())
		if err != nil {
			log.Printf("Fresh key usage bindings unavailable: %v", err)
			return false
		}
	}
	// The projection and identity workers maintain attribution independently of
	// the Collector authority, so they start once the keys are attributable.
	workerCtx := a.lifecycleContext(ctx)
	if !a.usageWorkersStarted {
		if a.startIdentityWorker != nil && !isCommittedExternalStrategy(checkpoint) {
			a.startIdentityWorker(workerCtx)
		}
		a.usageProjection.Start(workerCtx)
		a.usageWorkersStarted = true
	}
	if bound > 0 && a.usageProjection != nil {
		a.usageProjection.Wake()
	}

	var (
		waiter              *runObservationWaiter
		expectedRunID       uint64
		pendingRunBudgetMS  int64
		hasPendingRunBudget bool
	)
	// durable marker failure only retries the marker: StartRuntime cancels the
	// running Collector, so repeating it would open a restart window that loses
	// whatever arrived in between.
	if !a.usageCollectorStarted {
		if warmState.attempted && !warmState.started {
			a.usageStartupPending.Store(false)
			switch {
			case warmState.managedSourceErr:
				a.setUsageStartReason(usageStartReasonNoAuthority)
			case warmState.statisticsDisabled:
				a.setUsageStartReason(usageStartReasonStatisticsDisabled)
			default:
				a.setUsageStartReason(usageStartReasonCollectorFailed)
			}
			return false
		}
		enabled, err := a.usageMonitoringEnabled(ctx)
		if err != nil {
			log.Printf("Fresh request monitoring switch unavailable: %v", err)
			a.usageStartupPending.Store(false)
			a.setUsageStartReason(usageStartReasonCollectorFailed)
			return false
		}
		if !enabled {
			a.ensureUsageMonitoringDisabledGap(ctx)
			a.usageStartupPending.Store(false)
			a.setUsageStartReason(usageStartReasonMonitoringDisabled)
			return false
		}
		started := false
		switch {
		case a.privateCollector != nil:
			started = a.privateCollector(workerCtx)
		case isCommittedExternalStrategy(checkpoint) && a.externalSource != nil:
			if a.legacyCollector != nil {
				expectedRunID = a.legacyCollector.StartCount() + 1
				waiter = a.registerRunObservationWaiterLocked(expectedRunID)
				a.registerRunRetentionPendingLocked(expectedRunID)
			}
			a.usageStartupPending.Store(true)
			outcome := a.startExternalUsageCollector(workerCtx)
			a.setUsageStartReason(outcome.Reason)
			if !outcome.Started {
				a.usageStartupPending.Store(false)
				if expectedRunID != 0 {
					a.failRunRetention(expectedRunID, errors.New(outcome.Reason))
					a.clearRunObservationWaiterLocked(expectedRunID)
				}
				return false
			}
			if outcome.HasBudget {
				pendingRunBudgetMS = outcome.BudgetMS
				hasPendingRunBudget = true
			}
			started = true
		case a.managedSource != nil:
			install, err := a.managedSource(ctx)
			if err != nil {
				log.Printf("Fresh usage collection unavailable: %v", err)
				a.usageStartupPending.Store(false)
				a.setUsageStartReason(usageStartReasonNoAuthority)
				return false
			}
			runtimeCfg, err := a.usageRuntimeConfig(ctx, install.BaseURL, install.ManagementKey)
			if err != nil {
				log.Printf("Fresh usage collection configuration unavailable: %v", err)
				a.usageStartupPending.Store(false)
				a.setUsageStartReason(usageStartReasonCollectorFailed)
				return false
			}
			if a.runtime != nil {
				status, statusErr := a.observeInstallationRuntime(ctx)
				if statusErr != nil || status.State != model.RuntimeStateReady {
					a.setUsageStartReason(usageStartReasonCollectorFailed)
					a.usageStartupPending.Store(true)
					a.armManagedUsageRuntimeWaiterLocked(workerCtx)
					return false
				}
			}
			runtimeCfg.SourceScope = managedSourceScope(op)
			a.activeExternalGrant.Store(activeCollectorGrant{})
			expectedRunID = a.legacyCollector.StartCount() + 1
			a.obsMu.Lock()
			a.warmStartPending = false
			a.pendingWarmObs = nil
			a.obsMu.Unlock()
			waiter = a.registerRunObservationWaiterLocked(expectedRunID)
			a.registerRunRetentionPendingLocked(expectedRunID)
			a.usageStartupPending.Store(true)
			if err := a.legacyCollector.StartRuntime(workerCtx, runtimeCfg); err != nil {
				a.failRunRetention(expectedRunID, err)
				a.usageStartupPending.Store(false)
				a.clearRunObservationWaiterLocked(expectedRunID)
				log.Printf("Fresh usage collection unavailable: %v", err)
				a.setUsageStartReason(usageStartReasonCollectorFailed)
				return false
			}
			usageCfg, err := a.sampleUsageAuthority(ctx, install.BaseURL, install.ManagementKey)
			if err != nil {
				a.failRunRetention(expectedRunID, err)
				waitStop := a.legacyCollector.CancelRun(expectedRunID)
				a.usageStartupPending.Store(false)
				a.clearRunObservationWaiterLocked(expectedRunID)
				log.Printf("Fresh usage collection sample unavailable: %v", err)
				a.setUsageStartReason(usageStartReasonCollectorFailed)
				a.usageStartMu.Unlock()
				_ = waitStop(ctx)
				a.usageStartMu.Lock()
				return false
			}
			if usageCfg.HasUsageStatisticsEnabled && !usageCfg.UsageStatisticsEnabled {
				if a.usage != nil {
					_ = a.usage.RecordUnreplayedGap(ctx)
				}
				statsErr := errors.New(usageStartReasonStatisticsDisabled)
				a.failRunRetention(expectedRunID, statsErr)
				waitStop := a.legacyCollector.CancelRun(expectedRunID)
				a.usageStartupPending.Store(false)
				a.clearRunObservationWaiterLocked(expectedRunID)
				log.Printf("Fresh usage collection statistics disabled on managed CPA")
				a.setUsageStartReason(usageStartReasonStatisticsDisabled)
				a.usageStartMu.Unlock()
				_ = waitStop(ctx)
				a.usageStartMu.Lock()
				return false
			}
			started = a.rawCollectionPhase() != usageapp.PhaseStopped
			if !started {
				a.failRunRetention(expectedRunID, errors.New("collector stopped"))
				waitStop := a.legacyCollector.CancelRun(expectedRunID)
				a.usageStartupPending.Store(false)
				a.clearRunObservationWaiterLocked(expectedRunID)
				a.setUsageStartReason(usageStartReasonCollectorFailed)
				a.usageStartMu.Unlock()
				_ = waitStop(ctx)
				a.usageStartMu.Lock()
				return false
			} else {
				pendingRunBudgetMS = collector.VerifiedRetentionBudgetMS(usageCfg)
				hasPendingRunBudget = true
			}
		default:
			a.usageStartupPending.Store(false)
			a.setUsageStartReason(usageStartReasonNoAuthority)
			return false
		}
		if !started {
			a.usageStartupPending.Store(false)
			if expectedRunID != 0 {
				a.clearRunObservationWaiterLocked(expectedRunID)
			}
			return false
		}
		a.usageCollectorStarted = true
		a.setUsageStartReason("")
	}

	clearWarmStartOnReturn := true
	defer func() {
		if !clearWarmStartOnReturn {
			return
		}
		a.obsMu.Lock()
		a.warmStartPending = false
		a.pendingWarmObs = nil
		a.obsMu.Unlock()
	}()
	stillEnabled, enabledErr := a.usageMonitoringEnabled(ctx)
	if enabledErr != nil {
		if hasPendingRunBudget && expectedRunID != 0 {
			a.failRunRetention(expectedRunID, enabledErr)
		}
		waitStop := a.stopCurrentUsageCollectionLocked(ctx)
		a.setUsageStartReason(usageStartReasonCollectorFailed)
		log.Printf("Fresh request monitoring switch unavailable: %v", enabledErr)
		clearWarmStartOnReturn = false
		a.usageStartMu.Unlock()
		if waitStop != nil {
			_ = waitStop(ctx)
		}
		a.usageStartMu.Lock()
		return false
	}
	if !stillEnabled {
		a.ensureUsageMonitoringDisabledGap(ctx)
		if hasPendingRunBudget && expectedRunID != 0 {
			a.failRunRetention(expectedRunID, errors.New(usageStartReasonMonitoringDisabled))
		}
		waitStop := a.stopCurrentUsageCollectionLocked(ctx)
		a.setUsageStartReason(usageStartReasonMonitoringDisabled)
		clearWarmStartOnReturn = false
		a.usageStartMu.Unlock()
		if waitStop != nil {
			_ = waitStop(ctx)
		}
		a.usageStartMu.Lock()
		return false
	}
	if hasPendingRunBudget && expectedRunID != 0 {
		a.legacyCollector.SetRunRetentionBudget(expectedRunID, pendingRunBudgetMS)
		a.resolveRunRetention(expectedRunID, pendingRunBudgetMS)
	}
	snap, snapErr := a.readUsageSettingsSnapshot(ctx)
	if snapErr != nil {
		log.Printf("Fresh usage settings snapshot unavailable: %v", snapErr)
		return false
	}

	if a.privateCollector != nil {
		var startErr error
		if snap.disabledGap || snap.unreplayedGap {
			startErr = a.usage.AdvanceCollectionStart(ctx)
		} else {
			startErr = a.usage.MarkCollectionStart(ctx)
		}
		if startErr != nil {
			log.Printf("Fresh usage collection start unavailable: %v", startErr)
			return false
		}
		a.usageStartupPending.Store(false)
		a.lastStartedDisableSeq = snap.disableSeq
		return true
	}

	if warmState.started && waiter == nil && (a.legacyCollector == nil || warmState.runID == 0 || a.legacyCollector.StartCount() == warmState.runID) {
		a.obsMu.Lock()
		a.warmStartPending = false
		warmObs := a.pendingWarmObs
		a.pendingWarmObs = nil
		warmWaiter := a.runObsWaiters[warmState.runID]
		var warmWaitErr error
		warmWaitDone := false
		if warmWaiter != nil && warmWaiter.done {
			warmWaitDone = true
			warmWaitErr = warmWaiter.err
		}
		a.obsMu.Unlock()

		if warmObs != nil && a.rawCollectionPhase() != usageapp.PhaseStopped {
			if hook := a.beforeUsageMarkerCommit; hook != nil {
				hook()
			}
			if err := a.recordObservation(ctx, *warmObs); err != nil {
				a.usageStartupPending.Store(false)
				log.Printf("Fresh usage collection start unavailable: %v", err)
				return false
			}
			a.usageStartupPending.Store(false)
		} else if warmWaitDone && warmWaitErr != nil {
			a.usageStartupPending.Store(false)
		} else if warmWaiter != nil && a.rawCollectionPhase() != usageapp.PhaseStopped {
			done, obsErr := a.waitForRunObservation(ctx, warmWaiter, 2*time.Second)
			if done && obsErr != nil {
				a.usageStartupPending.Store(false)
				log.Printf("Fresh usage collection start unavailable: %v", obsErr)
				return false
			}
		}
		if freshSnap, err := a.readUsageSettingsSnapshot(ctx); err == nil {
			snap = freshSnap
		}
		a.lastStartedDisableSeq = snap.disableSeq
		return true
	}
	a.obsMu.Lock()
	a.warmStartPending = false
	a.pendingWarmObs = nil
	a.obsMu.Unlock()

	waitTimeout := 2 * time.Second
	if fromWaiter {
		waitTimeout = 150 * time.Millisecond
	}

	if waiter != nil {
		done, obsErr := a.waitForRunObservation(ctx, waiter, waitTimeout)
		if done && obsErr != nil {
			a.obsMu.Lock()
			observedThisRun := a.lastObserved.RunID == expectedRunID && a.lastObserved.ObservedAtMS > 0
			a.obsMu.Unlock()
			a.usageStartupPending.Store(false)
			if observedThisRun {
				log.Printf("Fresh usage collection start unavailable: %v", obsErr)
				return false
			}
		}
		if freshSnap, err := a.readUsageSettingsSnapshot(ctx); err == nil {
			snap = freshSnap
		}
		a.lastStartedDisableSeq = snap.disableSeq
		return true
	}

	// Retry path after a durable marker write failure while Collector stayed live.
	var currentRunID uint64
	if a.legacyCollector != nil {
		currentRunID = a.legacyCollector.StartCount()
	}
	a.obsMu.Lock()
	obs := a.lastObserved
	var existingWaiter *runObservationWaiter
	if currentRunID != 0 {
		existingWaiter = a.runObsWaiters[currentRunID]
	}
	a.obsMu.Unlock()

	if obs.ObservedAtMS > 0 && (currentRunID == 0 || obs.RunID == currentRunID) {
		if hook := a.beforeUsageMarkerCommit; hook != nil {
			hook()
		}
		if err := a.recordObservation(ctx, obs); err != nil {
			a.usageStartupPending.Store(false)
			log.Printf("Fresh usage collection start unavailable: %v", err)
			return false
		}
		a.usageStartupPending.Store(false)
		if freshSnap, err := a.readUsageSettingsSnapshot(ctx); err == nil {
			snap = freshSnap
		}
		a.lastStartedDisableSeq = snap.disableSeq
		return true
	}
	if existingWaiter != nil {
		done, obsErr := a.waitForRunObservation(ctx, existingWaiter, waitTimeout)
		if done && obsErr != nil {
			a.obsMu.Lock()
			observedThisRun := a.lastObserved.RunID == currentRunID && a.lastObserved.ObservedAtMS > 0
			a.obsMu.Unlock()
			a.usageStartupPending.Store(false)
			if observedThisRun {
				log.Printf("Fresh usage collection start unavailable: %v", obsErr)
				return false
			}
		}
	}
	if freshSnap, err := a.readUsageSettingsSnapshot(ctx); err == nil {
		snap = freshSnap
	}
	a.lastStartedDisableSeq = snap.disableSeq
	return true
}

func (a *freshApplication) cancelManagedUsageRuntimeWaiterLocked() {
	a.usageAwaitID++
	a.usageAwaitArmed.Store(false)
	if a.usageAwaitCancel != nil {
		a.usageAwaitCancel()
		a.usageAwaitCancel = nil
	}
}

func (a *freshApplication) armManagedUsageRuntimeWaiterLocked(ctx context.Context) {
	if a.runtime == nil || a.store == nil || a.usageAwaitCancel != nil || ctx == nil || ctx.Err() != nil {
		return
	}
	waitCtx, cancel := context.WithCancel(a.lifecycleContext(ctx))
	a.usageAwaitID++
	waiterID := a.usageAwaitID
	a.usageAwaitCancel = cancel
	a.usageAwaitArmed.Store(true)
	a.usageStartupPending.Store(true)
	if !a.launchBackground(func() { a.awaitManagedRuntimeForUsage(waitCtx, cancel, waiterID) }) {
		a.usageAwaitArmed.Store(false)
		a.usageStartupPending.Store(false)
		cancel()
		a.usageAwaitCancel = nil
	}
}

func (a *freshApplication) startUsageCollectionFromWaiter(waiterID uint64, op bootstrap.FreshOperation) {
	if a == nil || a.usageProjection == nil || a.usage == nil {
		return
	}
	runCtx := a.runCtx
	if runCtx == nil || a.isClosing() {
		log.Printf("Fresh usage workers not started: no host lifetime")
		return
	}
	if hook := a.beforeUsageStartLock; hook != nil {
		hook()
	}
	a.usageStartMu.Lock()
	defer a.usageStartMu.Unlock()
	if a.isClosing() {
		a.usageStartupPending.Store(false)
		return
	}
	a.startDatabaseMaintenance(a.lifecycleContext(runCtx))
	if a.usageAwaitID != waiterID || a.usageStarted {
		return
	}
	if a.usageAwaitCancel != nil {
		a.usageAwaitCancel()
		a.usageAwaitCancel = nil
	}
	a.usageAwaitArmed.Store(false)
	a.usageAwaitID++
	if a.attemptUsageStart(runCtx, op, true) {
		a.usageStarted = true
		a.cancelManagedUsageRuntimeWaiterLocked()
	}
}

func (a *freshApplication) releaseManagedUsageRuntimeWaiter(waiterID uint64) {
	a.usageStartMu.Lock()
	defer a.usageStartMu.Unlock()
	if a.usageAwaitID == waiterID {
		if a.usageAwaitCancel != nil {
			a.usageAwaitCancel()
			a.usageAwaitCancel = nil
		}
		a.usageAwaitArmed.Store(false)
		a.usageStartupPending.Store(false)
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
		a.startUsageCollectionFromWaiter(waiterID, record.Operation)
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
	fromMS, toMS, err := usageWindow(r.URL.Query())
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	ids, err := a.keys.IdentityIDs(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	summary, err := a.usage.SummaryWindow(r.Context(), ids, fromMS, toMS)
	if errors.Is(err, usageapp.ErrWindow) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
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
