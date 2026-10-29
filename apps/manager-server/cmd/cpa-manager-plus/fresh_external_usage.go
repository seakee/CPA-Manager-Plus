package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	usageapp "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
)

// Stable codes of a Collector start this Manager refused to claim. They reach
// the browser and never carry an address, a credential or a raw error.
const (
	usageStartReasonNoAuthority         = "usage_collection_authority_unavailable"
	usageStartReasonCollectorFailed     = "usage_collector_unavailable"
	usageStartReasonStatisticsDisabled  = "usage_statistics_disabled"
	usageStartReasonExternalUnavailable = "external_cpa_authority_unavailable"
)

// usageStartOutcome is what one Collector startup attempt proved. Started is
// true only when a Collector is running on an authority whose credential
// answered an authenticated sample read; every other outcome names why nothing
// started, and a refused start records no collection window.
type usageStartOutcome struct {
	Started bool
	Reason  string
}

// usageRuntimeConfig is the Collector configuration of one resolved CPA
// authority plus the Manager-wide transport settings.
func (a *freshApplication) usageRuntimeConfig(baseURL, managementKey string) collector.RuntimeConfig {
	return collector.RuntimeConfig{
		CPAUpstreamURL: baseURL,
		ManagementKey:  managementKey,
		CollectorMode:  a.cfg.CollectorMode,
		Queue:          a.cfg.Queue,
		PopSide:        a.cfg.PopSide,
		BatchSize:      a.cfg.BatchSize,
		PollInterval:   a.cfg.PollInterval,
		TLSSkipVerify:  a.cfg.TLSSkipVerify,
	}
}

// sampleUsageAuthority proves, with one authenticated management read, that the
// credential the Collector is about to use still authenticates. The Collector
// reports a live phase before it consumed anything, so its phase alone is not
// evidence: this read is.
func (a *freshApplication) sampleUsageAuthority(ctx context.Context, baseURL, managementKey string) error {
	_, err := cpa.ObserveManagementAPI(ctx, baseURL, managementKey)
	return err
}

// stopUsageCollection stops the Collector and clears the latched start so a
// later attempt may run again. It is used once the credential has been replaced:
// the refusal that latched the previous start is not a permanent property of the
// installation, and a Collector still polling the replaced credential would be
// one more failed authentication per attempt. Both success flags are cleared,
// because a rebind must be able to restart a Collector this process already
// started: the collection instant itself is idempotent and keeps its earliest
// value.
func (a *freshApplication) stopUsageCollection(ctx context.Context) {
	if a.legacyCollector != nil {
		_ = a.legacyCollector.Stop(ctx)
	}
	a.usageStartMu.Lock()
	defer a.usageStartMu.Unlock()
	a.usageStarted = false
	a.usageCollectorStarted = false
	a.setUsageStartReason("")
}

// resetUsageStart clears the latched start without touching a running Collector.
// The administrator's confirmation changes the premise the previous attempt was
// refused on, so the start may run again; the Collector itself is left alone
// because restarting a live one would open the gap this Manager avoids.
func (a *freshApplication) resetUsageStart() {
	a.usageStartMu.Lock()
	defer a.usageStartMu.Unlock()
	a.usageStarted = false
	a.usageCollectorStarted = false
	a.setUsageStartReason("")
}

// setUsageStartReason records the stable code of the last refused start. It is
// readable while the start is refused, so the workspace reports why coverage is
// missing instead of presenting an unexplained empty window.
func (a *freshApplication) setUsageStartReason(reason string) {
	a.usageStartReason.Store(reason)
}

// usageStartBlock reports the stable code of the last refused start, if any.
func (a *freshApplication) usageStartBlock() string {
	reason, _ := a.usageStartReason.Load().(string)
	return reason
}

// usageCollectionStarted reports whether this process proved a live Collector
// for the committed authority. It reads the same latch a fully successful start
// sets, never the absence of a refusal: a start that was simply never attempted
// has no blocker, so an unstarted window must not read as coverage.
func (a *freshApplication) usageCollectionStarted() bool {
	a.usageStartMu.Lock()
	defer a.usageStartMu.Unlock()
	return a.usageCollectorStarted
}

// startExternalUsageCollector arms collection against the user's own CPA. The
// committed External authority resolves the address and the decrypted
// credential; nothing reads the Manager configuration directly, so an
// uncommitted or foreign connection can never become a collection source.
//
// Enabling usage statistics writes to a CPA this Manager does not own, so it
// happens only after the administrator's explicit confirmation and is proven by
// reading the switch back. A refused credential stops collection outright: only
// rebinding the connection resolves it, so it is never a retry reason.
func (a *freshApplication) startExternalUsageCollector(ctx context.Context) usageStartOutcome {
	if a.externalSource == nil || a.legacyCollector == nil {
		return usageStartOutcome{Reason: usageStartReasonNoAuthority}
	}
	install, err := a.externalSource(ctx)
	if err != nil {
		if errors.Is(err, cpa.ErrManagementRejected) {
			return a.refuseExternalUsage(ctx, freshconfig.ReasonManagementRejected)
		}
		log.Printf("Fresh external usage collection unavailable: %v", err)
		return usageStartOutcome{Reason: usageStartReasonExternalUnavailable}
	}
	// Collection is only worth arming on a CPA this Manager can prove it manages:
	// the version contract decides the enable write and every later write, so a
	// CPA that does not serve it starts nothing. The refusal is the same typed
	// code the write surfaces answer with, never an unexplained empty window.
	switch report, err := cpa.ObserveExternalVersion(ctx, install.BaseURL, install.ManagementKey); {
	case errors.Is(err, cpa.ErrManagementRejected):
		return a.refuseExternalUsage(ctx, freshconfig.ReasonManagementRejected)
	case err != nil:
		log.Printf("Fresh external usage version unavailable: %v", err)
		return usageStartOutcome{Reason: usageStartReasonExternalUnavailable}
	case report.WriteBlocked:
		return a.refuseExternalUsage(ctx, freshconfig.ReasonVersionUnsupported)
	}
	// The first authenticated sample of this attempt is also the read of the
	// switch that decides whether anything has to be written at all.
	enabled, err := cpa.FetchExternalUsageStatistics(ctx, install.BaseURL, install.ManagementKey)
	switch {
	case errors.Is(err, cpa.ErrManagementRejected):
		return a.refuseExternalUsage(ctx, freshconfig.ReasonManagementRejected)
	case err != nil:
		log.Printf("Fresh external usage statistics unavailable: %v", err)
		return usageStartOutcome{Reason: usageStartReasonExternalUnavailable}
	case !enabled:
		// The confirmation is the authorisation of one write, so it is consumed
		// by the attempt that reads it: a later replay of this completion, or a
		// restart, asks again instead of reusing an approval nobody renewed. It
		// is consumed before the write so a failed enable cannot silently keep
		// an approval alive for the next replay.
		if !a.externalUsageConfirmed.CompareAndSwap(true, false) {
			// Without the confirmation nothing is written to the user's CPA and
			// nothing is collected, so the window stays unstarted on purpose.
			return usageStartOutcome{Reason: usageStartReasonStatisticsDisabled}
		}
		if err = cpa.SetExternalUsageStatistics(ctx, install.BaseURL, install.ManagementKey, true); err != nil {
			if errors.Is(err, cpa.ErrManagementRejected) {
				return a.refuseExternalUsage(ctx, freshconfig.ReasonManagementRejected)
			}
			log.Printf("Fresh external usage statistics enable unavailable: %v", err)
			return usageStartOutcome{Reason: usageStartReasonExternalUnavailable}
		}
		// The write is proven, never assumed: an unproven switch would let the
		// Collector run against a CPA that records nothing.
		back, err := cpa.FetchExternalUsageStatistics(ctx, install.BaseURL, install.ManagementKey)
		switch {
		case errors.Is(err, cpa.ErrManagementRejected):
			return a.refuseExternalUsage(ctx, freshconfig.ReasonManagementRejected)
		case err != nil || !back:
			log.Printf("Fresh external usage statistics enable was not observed: %v", err)
			return usageStartOutcome{Reason: usageStartReasonStatisticsDisabled}
		}
	}
	// Staged keys are attributed to the connection identity, never to a raw
	// address and never to the managed destination: another CPA is another
	// identity, so a rebind can never inherit this history.
	if _, err := a.keyRepo.BindFreshKeySources(ctx, install.Identity, time.Now().UnixMilli()); err != nil {
		log.Printf("Fresh external key usage bindings unavailable: %v", err)
		return usageStartOutcome{Reason: usageStartReasonExternalUnavailable}
	}
	if err := a.legacyCollector.StartRuntime(ctx, a.usageRuntimeConfig(install.BaseURL, install.ManagementKey)); err != nil {
		log.Printf("Fresh external usage collection unavailable: %v", err)
		return usageStartOutcome{Reason: usageStartReasonCollectorFailed}
	}
	if a.collectionPhase() == usageapp.PhaseStopped {
		return usageStartOutcome{Reason: usageStartReasonCollectorFailed}
	}
	return usageStartOutcome{Started: true}
}

// refuseExternalUsage stops collection and reports the typed blocker. A refused
// management credential is terminal until the administrator rebinds the
// connection, so the Collector is stopped rather than retried.
func (a *freshApplication) refuseExternalUsage(ctx context.Context, reason string) usageStartOutcome {
	log.Printf("Fresh external usage collection stopped: %s", reason)
	if a.legacyCollector != nil {
		_ = a.legacyCollector.Stop(ctx)
	}
	return usageStartOutcome{Reason: reason}
}

// reportCollectorAuthRejection is the Collector's own report that the CPA
// refused the management credential it was running with. The Collector has
// already stopped itself, so this records the blocker and clears the started
// latch: a live Collector that kept retrying a rotated credential would turn the
// user's rotation into an address ban, and a latched start would keep the
// workspace claiming coverage that no longer exists.
func (a *freshApplication) reportCollectorAuthRejection() {
	a.usageStartMu.Lock()
	defer a.usageStartMu.Unlock()
	a.usageCollectorStarted = false
	a.usageStarted = false
	a.setUsageStartReason(freshconfig.ReasonManagementRejected)
	log.Printf("Fresh external usage collection stopped: %s", freshconfig.ReasonManagementRejected)
}

// externalUsageRoute serves the External usage workspace: the collection state
// and its administrator confirmation at the root, and the CPA version report
// beneath it. Both are reads of the user's own CPA, so a refused credential is
// the typed rebind blocker and never a silent empty answer.
func (a *freshApplication) externalUsageRoute(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation, suffix string) {
	if suffix != "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !op.ProductComplete || a.externalSource == nil {
		w.WriteHeader(http.StatusConflict)
		return
	}
	if r.Method == http.MethodPost {
		a.confirmExternalUsageCollection(w, r, op)
		return
	}
	state, err := a.externalUsageState(r.Context())
	if err != nil {
		a.writeExternalUsageError(w, err)
		return
	}
	writeFreshJSON(w, state)
}

// confirmExternalUsageCollection is the administrator's explicit approval of
// collection. It is the only path that may enable usage statistics on a CPA
// this Manager does not own, so the confirmation is a required field rather than
// a default.
func (a *freshApplication) confirmExternalUsageCollection(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation) {
	if r.Header.Get("Content-Type") != "application/json" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	var request struct {
		RequestID               string `json:"requestId"`
		ConfirmEnableStatistics bool   `json:"confirmEnableStatistics"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF ||
		strings.TrimSpace(request.RequestID) == "" || !request.ConfirmEnableStatistics {
		// A request without the confirmation would enable a switch the
		// administrator never approved, so nothing is started.
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// A confirm while collection is already live is a no-op: the switch it would
	// authorise is already on, and restarting the Collector here would cancel the
	// running one and open the window this Manager works to avoid. The started
	// latch is left exactly as it is.
	if a.usageCollectionStarted() {
		state, err := a.externalUsageState(r.Context())
		if err != nil {
			a.writeExternalUsageError(w, err)
			return
		}
		writeFreshJSON(w, state)
		return
	}
	a.externalUsageConfirmed.Store(true)
	// The refusal latched by an earlier attempt is not evidence about this one:
	// the administrator just changed the premise, so the start may run again.
	a.resetUsageStart()
	a.startUsageCollection(op)
	state, err := a.externalUsageState(r.Context())
	if err != nil {
		a.writeExternalUsageError(w, err)
		return
	}
	writeFreshJSON(w, state)
}

// externalVersionView is the CPA version this Manager observed and what it may do
// with it. It carries no credential and no address, and it is a report of one
// observation rather than a cached verdict.
type externalVersionView struct {
	Version      string `json:"version"`
	Support      string `json:"support"`
	WriteBlocked bool   `json:"writeBlocked"`
}

// externalUsageState is the External usage posture without any credential or
// address: what the user's CPA records, whether the Collector is live, which
// version it advertises, and the stable code of whatever blocked the last start.
type externalUsageState struct {
	StatisticsEnabled bool                `json:"statisticsEnabled"`
	Collector         string              `json:"collector"`
	CollectionStarted bool                `json:"collectionStarted"`
	CPA               externalVersionView `json:"cpaVersion"`
	BlockedReason     string              `json:"blockedReason,omitempty"`
}

func (a *freshApplication) externalUsageState(ctx context.Context) (externalUsageState, error) {
	install, err := a.externalSource(ctx)
	if err != nil {
		return externalUsageState{}, err
	}
	// The CPA has already refused this credential, so this read would be another
	// failed authentication against an address CPA is about to ban. The typed
	// blocker is answered locally until a rebind or a re-verify proves the key
	// again.
	grant := a.externalReadGrant(ctx)
	if grant.Refused {
		return externalUsageState{}, externalReadRefusedError()
	}
	report, err := cpa.ObserveExternalVersion(ctx, install.BaseURL, install.ManagementKey)
	if err != nil {
		a.noteExternalRejection(ctx, grant, err)
		return externalUsageState{}, err
	}
	enabled, err := cpa.FetchExternalUsageStatistics(ctx, install.BaseURL, install.ManagementKey)
	if err != nil {
		a.noteExternalRejection(ctx, grant, err)
		return externalUsageState{}, err
	}
	return externalUsageState{
		StatisticsEnabled: enabled,
		Collector:         string(a.collectionPhase()),
		CollectionStarted: a.usageCollectionStarted(),
		CPA:               externalVersionView{Version: report.Version, Support: report.Support, WriteBlocked: report.WriteBlocked},
		BlockedReason:     a.usageStartBlock(),
	}, nil
}

// writeExternalUsageError maps an External authority failure to its typed wire
// answer. A refused credential is the rebind blocker, distinct from an
// unavailable authority; neither carries any text from the CPA.
func (a *freshApplication) writeExternalUsageError(w http.ResponseWriter, err error) {
	code := usageStartReasonExternalUnavailable
	status := http.StatusServiceUnavailable
	if errors.Is(err, cpa.ErrManagementRejected) {
		status, code = http.StatusConflict, freshconfig.ReasonManagementRejected
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeFreshJSON(w, struct {
		Code string `json:"code"`
	}{code})
}
