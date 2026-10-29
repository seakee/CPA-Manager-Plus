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
	if checkpoint.Strategy != bootstrap.NewManaged || checkpoint.Stage != "committed" {
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
	a.usageStartMu.Lock()
	defer a.usageStartMu.Unlock()
	if a.usageStarted {
		return
	}
	if a.attemptUsageStart(runCtx, op) {
		a.usageStarted = true
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
	if checkpoint.Strategy == bootstrap.NewManaged && checkpoint.Stage == "committed" && checkpoint.TargetIdentity != "" {
		if _, err := a.keyRepo.BindFreshKeySources(ctx, checkpoint.TargetIdentity, time.Now().UnixMilli()); err != nil {
			log.Printf("Fresh key usage bindings unavailable: %v", err)
			return false
		}
	}
	// The projection and identity workers maintain attribution independently of
	// the Collector authority, so they start once the keys are attributable.
	if !a.usageWorkersStarted {
		if a.startIdentityWorker != nil {
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
		case checkpoint.Strategy == bootstrap.NewManaged && a.managedSource != nil && a.legacyCollector != nil:
			install, err := a.managedSource(ctx)
			if err != nil {
				// Managed and External installs have no other authority. Usage
				// stays unavailable instead of collecting from an unverified CPA.
				log.Printf("Fresh usage collection unavailable: %v", err)
				return false
			}
			if err := a.legacyCollector.StartRuntime(ctx, collector.RuntimeConfig{
				CPAUpstreamURL: install.BaseURL,
				ManagementKey:  install.ManagementKey,
				CollectorMode:  a.cfg.CollectorMode,
				Queue:          a.cfg.Queue,
				PopSide:        a.cfg.PopSide,
				BatchSize:      a.cfg.BatchSize,
				PollInterval:   a.cfg.PollInterval,
				TLSSkipVerify:  a.cfg.TLSSkipVerify,
			}); err != nil {
				log.Printf("Fresh usage collection unavailable: %v", err)
				return false
			}
			// A live phase is not proof: the Collector reports "starting" before
			// it has authenticated anything. The first authenticated management
			// read is, so it gates the durable collection instant.
			if err := a.sampleUsageAuthority(ctx, install.BaseURL, install.ManagementKey); err != nil {
				log.Printf("Fresh usage collection sample unavailable: %v", err)
				return false
			}
			started = a.collectionPhase() != usageapp.PhaseStopped
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
