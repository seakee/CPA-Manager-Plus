package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

const freshRuntimeJournalKey = "fresh_runtime_operations_v1"

const (
	// freshRuntimeJournalTerminalRetention bounds the durable journal to the most
	// recent terminal operations. Every non-terminal entry is always retained.
	freshRuntimeJournalTerminalRetention = 50
	// A lifecycle mutation can outlive the Manager's HTTP request budget: the
	// Supervisor explicitly drains accepted execution after the caller's
	// deadline. An entry younger than this window is treated as still in flight
	// and is never resolved behind the submitter's back.
	freshRuntimeOperationSettleWindow = 45 * time.Second
	// A submitted mutation and both of its durable follow-ups (desired lifecycle
	// and terminal journal entry) must survive a browser disconnect. The budget
	// stays above the Manager's own Runtime call timeout
	// (internal/service/runtime/embedded.go:30, 30s) and the Supervisor's drain
	// window on shutdown (apps/runtime-supervisor/cmd/cpamp-runtime-supervisor/
	// main.go:27, 10s), and below the settle window above so a concurrent poll
	// never resolves a mutation that is still running.
	freshRuntimeMutationBudget = 40 * time.Second
	// Runtime Protocol v1 exposes no lifecycle operation lookup and this resource
	// must never resend a mutation, so an entry that is still non-terminal after
	// this window can no longer be confirmed. The bounded rule makes it terminal
	// so a lost result can never lock the lifecycle out permanently.
	freshRuntimeOperationUnknownBound = 2 * time.Minute
)

// Journal states mirror the Runtime Protocol operation vocabulary plus the
// Manager-local `unknown` result of a mutation whose response was lost.
const (
	freshRuntimeStateAccepted  = string(model.RuntimeOperationAccepted)
	freshRuntimeStateSucceeded = string(model.RuntimeOperationSucceeded)
	freshRuntimeStateFailed    = string(model.RuntimeOperationFailed)
	freshRuntimeStateUnknown   = "unknown"
)

var freshRuntimeRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var freshRuntimeOperationIDPattern = regexp.MustCompile(`^[A-Za-z0-9_:/-]{1,192}$`)

type freshRuntimeJournal struct {
	db       *sql.DB
	mu       sync.Mutex
	last     *freshRuntimeStatusResponse
	lastTime time.Time
}

type freshRuntimeOperation struct {
	RequestID         string    `json:"requestId"`
	Action            string    `json:"action"`
	OperationID       string    `json:"operationId"`
	State             string    `json:"state"`
	RuntimeIdentity   string    `json:"runtimeIdentity,omitempty"`
	RuntimeGeneration uint64    `json:"runtimeGeneration,omitempty"`
	FailureCode       string    `json:"failureCode,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type freshRuntimeJournalData struct {
	Operations map[string]freshRuntimeOperation `json:"operations"`
}

func newFreshRuntimeJournal(db *sql.DB) *freshRuntimeJournal { return &freshRuntimeJournal{db: db} }

func (j *freshRuntimeJournal) load(ctx context.Context) (freshRuntimeJournalData, error) {
	data := freshRuntimeJournalData{Operations: map[string]freshRuntimeOperation{}}
	if j == nil || j.db == nil {
		return data, nil
	}
	var raw string
	err := j.db.QueryRowContext(ctx, `select value from settings where key = ?`, freshRuntimeJournalKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return data, nil
	}
	if err != nil {
		return data, err
	}
	if err := json.Unmarshal([]byte(raw), &data); err != nil || data.Operations == nil {
		return freshRuntimeJournalData{}, errors.New("runtime operation journal is unavailable")
	}
	return data, nil
}

// save persists the journal. Every write is bounded so the single settings row
// cannot grow without limit.
func (j *freshRuntimeJournal) save(ctx context.Context, data freshRuntimeJournalData) error {
	if j == nil || j.db == nil {
		return errors.New("runtime operation journal is unavailable")
	}
	raw, err := json.Marshal(freshRuntimeBoundOperations(data))
	if err != nil {
		return err
	}
	_, err = j.db.ExecContext(ctx, `insert into settings(key,value,updated_at_ms) values(?,?,?) on conflict(key) do update set value=excluded.value, updated_at_ms=excluded.updated_at_ms`, freshRuntimeJournalKey, string(raw), time.Now().UnixMilli())
	return err
}

func (j *freshRuntimeJournal) setLast(status *freshRuntimeStatusResponse, observedAt time.Time) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.last = status
	j.lastTime = observedAt
}

func (j *freshRuntimeJournal) lastStatus() (*freshRuntimeStatusResponse, time.Time) {
	if j == nil {
		return nil, time.Time{}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.last == nil {
		return nil, time.Time{}
	}
	snapshot := *j.last
	return &snapshot, j.lastTime
}

func freshRuntimeOperationTerminal(operation freshRuntimeOperation) bool {
	return operation.State == freshRuntimeStateSucceeded || operation.State == freshRuntimeStateFailed
}

func freshRuntimeOperationsPending(data freshRuntimeJournalData) bool {
	for _, operation := range data.Operations {
		if !freshRuntimeOperationTerminal(operation) {
			return true
		}
	}
	return false
}

// freshRuntimeBoundOperations keeps every non-terminal entry plus the most
// recent terminal ones. Journal compaction must never hide an operation whose
// outcome the page can still be waiting for.
func freshRuntimeBoundOperations(data freshRuntimeJournalData) freshRuntimeJournalData {
	bounded := freshRuntimeJournalData{Operations: make(map[string]freshRuntimeOperation, len(data.Operations))}
	terminal := make([]freshRuntimeOperation, 0, len(data.Operations))
	for requestID, operation := range data.Operations {
		if !freshRuntimeOperationTerminal(operation) {
			bounded.Operations[requestID] = operation
			continue
		}
		terminal = append(terminal, operation)
	}
	if len(terminal) > freshRuntimeJournalTerminalRetention {
		sort.Slice(terminal, func(left, right int) bool {
			if terminal[left].UpdatedAt.Equal(terminal[right].UpdatedAt) {
				return terminal[left].RequestID < terminal[right].RequestID
			}
			return terminal[left].UpdatedAt.After(terminal[right].UpdatedAt)
		})
		terminal = terminal[:freshRuntimeJournalTerminalRetention]
	}
	for _, operation := range terminal {
		bounded.Operations[operation.RequestID] = operation
	}
	return bounded
}

// freshRuntimeOperationBlocks reports whether a non-terminal entry still owns
// the lifecycle. Only entries younger than the unknown bound may block: an older
// entry is abandoned by the bounded resolution rule and must not keep a
// different action out forever.
func freshRuntimeOperationBlocks(data freshRuntimeJournalData, candidate freshRuntimeOperation) bool {
	for _, existing := range data.Operations {
		if freshRuntimeOperationTerminal(existing) || existing.Action == candidate.Action {
			continue
		}
		if candidate.CreatedAt.Sub(existing.UpdatedAt) >= freshRuntimeOperationUnknownBound {
			continue
		}
		return true
	}
	return false
}

// freshRuntimeResolveOperation advances one non-terminal entry from a Runtime
// observation. It never resubmits the mutation; resolution requires either an
// observation that distinguishes the outcome or the bounded expiry rule.
func freshRuntimeResolveOperation(
	operation freshRuntimeOperation,
	observed model.RuntimeObservedStatus,
	observedOK bool,
	now time.Time,
) (freshRuntimeOperation, bool) {
	if freshRuntimeOperationTerminal(operation) {
		return operation, false
	}
	age := now.Sub(operation.UpdatedAt)
	if age < freshRuntimeOperationSettleWindow {
		return operation, false
	}
	expire := func(code string) (freshRuntimeOperation, bool) {
		operation.State = freshRuntimeStateFailed
		operation.FailureCode = code
		operation.UpdatedAt = now
		return operation, true
	}
	succeed := func() (freshRuntimeOperation, bool) {
		operation.State = freshRuntimeStateSucceeded
		operation.FailureCode = ""
		operation.UpdatedAt = now
		return operation, true
	}
	if !observedOK {
		if age >= freshRuntimeOperationUnknownBound {
			return expire("unknown_expired")
		}
		return operation, false
	}
	// A different Runtime identity means this Manager can no longer see the
	// Supervisor that accepted the operation.
	if operation.RuntimeIdentity != "" && observed.Identity != "" && string(observed.Identity) != operation.RuntimeIdentity {
		return expire("runtime_identity_changed")
	}
	switch operation.Action {
	case string(model.RuntimeOperationStop):
		if observed.State == model.RuntimeStateOffline {
			return succeed()
		}
	case string(model.RuntimeOperationStart):
		if observed.State == model.RuntimeStateReady || observed.State == model.RuntimeStateStarting {
			return succeed()
		}
	}
	// A lost Restart result cannot be confirmed from an observation: the
	// Supervisor mints runtimeGeneration once per process from crypto/rand
	// (apps/runtime-supervisor/cmd/cpamp-runtime-supervisor/main.go:142-148) and
	// keeps that fixed authority through Restart
	// (apps/runtime-supervisor/internal/lifecycle/restart.go:72-105), so a
	// completed restart leaves both generation and identity unchanged. Since a
	// restored lifecycle is equally consistent with the restart never running,
	// the entry expires as unknown below rather than being guessed succeeded.
	if age >= freshRuntimeOperationUnknownBound {
		return expire("unknown_expired")
	}
	return operation, false
}

var errEmbeddedRuntimeDisabled = errors.New("CPA runtime client is not configured")

type freshEmbeddedExecutionGate struct {
	mu                 sync.Mutex
	inFlight           int
	drained            chan struct{}
	committingExternal bool
	closed             bool
	cancels            map[uint64]context.CancelFunc
	nextID             uint64
	externalCommitted  chan struct{}
	reconcilerCancel   context.CancelFunc
	reconcilerDone     chan struct{}
}

func (a *freshApplication) enterEmbeddedExecution(ctx context.Context) (context.Context, func(), error) {
	if a == nil || a.runtime == nil {
		return nil, nil, errEmbeddedRuntimeDisabled
	}
	gate := &a.embeddedExecution
	gate.mu.Lock()
	if gate.closed || gate.committingExternal {
		gate.mu.Unlock()
		return nil, nil, errEmbeddedRuntimeDisabled
	}
	callCtx, cancel := context.WithCancel(ctx)
	gate.nextID++
	id := gate.nextID
	if gate.cancels == nil {
		gate.cancels = make(map[uint64]context.CancelFunc)
	}
	gate.cancels[id] = cancel
	if gate.inFlight == 0 {
		gate.drained = make(chan struct{})
	}
	gate.inFlight++
	gate.mu.Unlock()

	var once sync.Once
	release := func() {
		once.Do(func() {
			cancel()
			gate.mu.Lock()
			delete(gate.cancels, id)
			gate.inFlight--
			if gate.inFlight == 0 && gate.drained != nil {
				close(gate.drained)
				gate.drained = nil
			}
			gate.mu.Unlock()
		})
	}

	if a.store != nil {
		op, err := a.current(callCtx)
		if err != nil {
			release()
			return nil, nil, err
		}
		if op.Strategy.Strategy == bootstrap.ConnectExternal {
			a.markEmbeddedExecutionClosed()
			release()
			return nil, nil, errEmbeddedRuntimeDisabled
		}
	}
	if callCtx.Err() != nil {
		release()
		return nil, nil, errEmbeddedRuntimeDisabled
	}
	return callCtx, release, nil
}

func (a *freshApplication) externalCommittedSignal() <-chan struct{} {
	gate := &a.embeddedExecution
	gate.mu.Lock()
	if gate.externalCommitted == nil {
		gate.externalCommitted = make(chan struct{})
		if gate.closed {
			close(gate.externalCommitted)
		}
	}
	ch := gate.externalCommitted
	gate.mu.Unlock()
	return ch
}

func (a *freshApplication) cancelInFlightEmbeddedCallsLocked() chan struct{} {
	gate := &a.embeddedExecution
	for id, cancel := range gate.cancels {
		cancel()
		delete(gate.cancels, id)
	}
	if gate.inFlight > 0 {
		return gate.drained
	}
	return nil
}

func (a *freshApplication) retireEmbeddedReconcilerLocked() chan struct{} {
	gate := &a.embeddedExecution
	if gate.externalCommitted == nil {
		gate.externalCommitted = make(chan struct{})
	}
	select {
	case <-gate.externalCommitted:
	default:
		close(gate.externalCommitted)
	}
	if gate.reconcilerCancel != nil {
		gate.reconcilerCancel()
	}
	return gate.reconcilerDone
}

func (a *freshApplication) markEmbeddedExecutionClosed() {
	if a == nil {
		return
	}
	gate := &a.embeddedExecution
	gate.mu.Lock()
	gate.closed = true
	gate.committingExternal = false
	_ = a.cancelInFlightEmbeddedCallsLocked()
	_ = a.retireEmbeddedReconcilerLocked()
	gate.mu.Unlock()
}

func (a *freshApplication) beginExternalCommit() func(committed bool) {
	if a == nil {
		return func(bool) {}
	}
	gate := &a.embeddedExecution
	gate.mu.Lock()
	gate.committingExternal = true
	drained := a.cancelInFlightEmbeddedCallsLocked()
	gate.mu.Unlock()
	if drained != nil {
		<-drained
	}
	var once sync.Once
	return func(committed bool) {
		once.Do(func() {
			if !committed {
				gate.mu.Lock()
				if !gate.closed {
					gate.committingExternal = false
				}
				gate.mu.Unlock()
				return
			}
			gate.mu.Lock()
			gate.closed = true
			gate.committingExternal = false
			drainedAfter := a.cancelInFlightEmbeddedCallsLocked()
			reconcilerDone := a.retireEmbeddedReconcilerLocked()
			gate.mu.Unlock()
			if drainedAfter != nil {
				<-drainedAfter
			}
			if reconcilerDone != nil {
				<-reconcilerDone
			}
		})
	}
}

func (a *freshApplication) embeddedExecutionStopped() bool {
	if a == nil {
		return true
	}
	gate := &a.embeddedExecution
	gate.mu.Lock()
	stopped := gate.closed || gate.committingExternal
	gate.mu.Unlock()
	return stopped
}

func (a *freshApplication) embeddedRuntimeClient() runtimeservice.RuntimeClient {
	if a == nil || a.runtime == nil {
		return nil
	}
	return gatedEmbeddedRuntimeClient{app: a}
}

type gatedEmbeddedRuntimeClient struct {
	app *freshApplication
}

func (c gatedEmbeddedRuntimeClient) enter(ctx context.Context) (runtimeservice.RuntimeClient, context.Context, func(), error) {
	if c.app == nil || c.app.runtime == nil {
		return nil, nil, nil, errEmbeddedRuntimeDisabled
	}
	callCtx, release, err := c.app.enterEmbeddedExecution(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	client := c.app.runtime
	if client == nil {
		release()
		return nil, nil, nil, errEmbeddedRuntimeDisabled
	}
	return client, callCtx, release, nil
}
func (c gatedEmbeddedRuntimeClient) wrapErr(err error) error {
	if err != nil && (c.app == nil || c.app.embeddedExecutionStopped()) {
		return errEmbeddedRuntimeDisabled
	}
	return err
}

func (c gatedEmbeddedRuntimeClient) Status(ctx context.Context) (model.RuntimeObservedStatus, error) {
	client, callCtx, release, err := c.enter(ctx)
	if err != nil {
		return model.RuntimeObservedStatus{}, err
	}
	defer release()
	out, err := client.Status(callCtx)
	return out, c.wrapErr(err)
}

func (c gatedEmbeddedRuntimeClient) Start(ctx context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	client, callCtx, release, err := c.enter(ctx)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	defer release()
	out, err := client.Start(callCtx, request)
	return out, c.wrapErr(err)
}

func (c gatedEmbeddedRuntimeClient) Stop(ctx context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	client, callCtx, release, err := c.enter(ctx)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	defer release()
	out, err := client.Stop(callCtx, request)
	return out, c.wrapErr(err)
}

func (c gatedEmbeddedRuntimeClient) Restart(ctx context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	client, callCtx, release, err := c.enter(ctx)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	defer release()
	out, err := client.Restart(callCtx, request)
	return out, c.wrapErr(err)
}

func (c gatedEmbeddedRuntimeClient) PrepareUpdate(ctx context.Context, request model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
	client, callCtx, release, err := c.enter(ctx)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	defer release()
	out, err := client.PrepareUpdate(callCtx, request)
	return out, c.wrapErr(err)
}

func (c gatedEmbeddedRuntimeClient) ActivateUpdate(ctx context.Context, request model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
	client, callCtx, release, err := c.enter(ctx)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	defer release()
	out, err := client.ActivateUpdate(callCtx, request)
	return out, c.wrapErr(err)
}

func (c gatedEmbeddedRuntimeClient) ObserveUpdateOperation(ctx context.Context, request model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
	client, callCtx, release, err := c.enter(ctx)
	if err != nil {
		return model.RuntimeUpdateOperationObservation{}, err
	}
	defer release()
	out, err := client.ObserveUpdateOperation(callCtx, request)
	return out, c.wrapErr(err)
}

func (c gatedEmbeddedRuntimeClient) StageDestination(ctx context.Context, request model.RuntimeDestinationRequest) (model.RuntimeOperationResult, error) {
	client, callCtx, release, err := c.enter(ctx)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	defer release()
	dest, ok := client.(runtimeservice.DestinationClient)
	if !ok {
		return model.RuntimeOperationResult{}, errEmbeddedRuntimeDisabled
	}
	out, err := dest.StageDestination(callCtx, request)
	return out, c.wrapErr(err)
}

func (a *freshApplication) runtimeModeFor(op bootstrap.FreshOperation) model.RuntimeMode {
	if a == nil {
		return ""
	}
	if op.Strategy.Strategy == bootstrap.ConnectExternal {
		return model.RuntimeModeExternal
	}
	return model.RuntimeModeEmbedded
}

func (a *freshApplication) effectiveRuntimeMode(ctx context.Context) model.RuntimeMode {
	if a == nil || a.store == nil {
		return ""
	}
	op, err := a.current(ctx)
	if err != nil {
		return ""
	}
	return a.runtimeModeFor(op)
}

func (a *freshApplication) runtimeStatusClientFor(mode model.RuntimeMode) runtimeservice.RuntimeClient {
	if a == nil {
		return nil
	}
	switch mode {
	case model.RuntimeModeEmbedded:
		return a.embeddedRuntimeClient()
	case model.RuntimeModeExternal:
		return a.runtimeExternal
	default:
		return nil
	}
}

// runtimeStatusClient returns the read-only observation client for the active
// installation profile.
func (a *freshApplication) runtimeStatusClient() runtimeservice.RuntimeClient {
	return a.runtimeStatusClientFor(a.effectiveRuntimeMode(context.Background()))
}

func (a *freshApplication) observeExternalRuntimeStatus(ctx context.Context, client runtimeservice.RuntimeClient) (model.RuntimeObservedStatus, bool, error) {
	if a == nil || client == nil {
		return model.RuntimeObservedStatus{}, false, errEmbeddedRuntimeDisabled
	}
	gate := &a.legacyCredentialGate
	requestCtx := ctx
	if !hasExternalCredentialHold(ctx, gate) {
		if !gate.acquire(ctx) {
			return model.RuntimeObservedStatus{}, false, ctx.Err()
		}
		var hold *externalCredentialHold
		hold, requestCtx = beginExternalCredentialHold(ctx, gate, a.externalCredentialHeaderBudget())
		defer hold.finish()
	}
	grant := a.externalReadGrant(requestCtx)
	if grant.Refused {
		return model.RuntimeObservedStatus{}, true, externalReadRefusedError()
	}
	observed, err := client.Status(requestCtx)
	if err != nil {
		a.noteExternalRejection(context.WithoutCancel(requestCtx), grant, err)
		return model.RuntimeObservedStatus{}, false, err
	}
	return observed, false, nil
}

type freshDatabaseStatusProvider struct {
	dbPath string
}

func (p freshDatabaseStatusProvider) Snapshot() sqliterepo.WALMaintenanceSnapshot {
	return sqliterepo.InspectDatabaseFiles(p.dbPath)
}

type freshRuntimeClientAdapter struct {
	app *freshApplication
}

func (c freshRuntimeClientAdapter) embeddedClient(ctx context.Context) (runtimeservice.RuntimeClient, error) {
	if c.app != nil && c.app.effectiveRuntimeMode(ctx) == model.RuntimeModeEmbedded {
		if client := c.app.embeddedRuntimeClient(); client != nil {
			return client, nil
		}
	}
	return nil, errEmbeddedRuntimeDisabled
}

func (c freshRuntimeClientAdapter) Status(ctx context.Context) (model.RuntimeObservedStatus, error) {
	var mode model.RuntimeMode
	var client runtimeservice.RuntimeClient
	if c.app != nil {
		mode = c.app.effectiveRuntimeMode(ctx)
		client = c.app.runtimeStatusClientFor(mode)
	}
	if client == nil {
		return model.RuntimeObservedStatus{}, errEmbeddedRuntimeDisabled
	}
	if c.app != nil && mode == model.RuntimeModeExternal {
		observed, _, err := c.app.observeExternalRuntimeStatus(ctx, client)
		return observed, err
	}
	return client.Status(ctx)
}

func (c freshRuntimeClientAdapter) Start(ctx context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	client, err := c.embeddedClient(ctx)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	return client.Start(ctx, request)
}

func (c freshRuntimeClientAdapter) Stop(ctx context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	client, err := c.embeddedClient(ctx)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	return client.Stop(ctx, request)
}

func (c freshRuntimeClientAdapter) Restart(ctx context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	client, err := c.embeddedClient(ctx)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	return client.Restart(ctx, request)
}

func (c freshRuntimeClientAdapter) PrepareUpdate(ctx context.Context, request model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
	client, err := c.embeddedClient(ctx)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	return client.PrepareUpdate(ctx, request)
}

func (c freshRuntimeClientAdapter) ActivateUpdate(ctx context.Context, request model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
	client, err := c.embeddedClient(ctx)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	return client.ActivateUpdate(ctx, request)
}

func (c freshRuntimeClientAdapter) ObserveUpdateOperation(ctx context.Context, request model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
	client, err := c.embeddedClient(ctx)
	if err != nil {
		return model.RuntimeUpdateOperationObservation{}, err
	}
	return client.ObserveUpdateOperation(ctx, request)
}

// refreshRuntimeOperations resolves abandoned journal entries from one
// observation and returns the resulting journal. The observation and the journal
// read both happen outside the journal mutex; only the merge and write are
// serialized, so polling never waits on a Runtime network call.
func (a *freshApplication) refreshRuntimeOperations(
	ctx context.Context,
	observed *model.RuntimeObservedStatus,
) (freshRuntimeJournalData, error) {
	data, err := a.runtimeJournal.load(ctx)
	if err != nil {
		return data, err
	}
	if !freshRuntimeOperationsPending(data) {
		return data, nil
	}
	var status model.RuntimeObservedStatus
	observedOK := false
	if observed != nil {
		status, observedOK = *observed, true
	} else if client := a.embeddedRuntimeClient(); client != nil {
		status, err = client.Status(ctx)
		observedOK = err == nil
	}
	now := time.Now().UTC()
	transitions := make(map[string][2]freshRuntimeOperation)
	for requestID, operation := range data.Operations {
		next, changed := freshRuntimeResolveOperation(operation, status, observedOK, now)
		if !changed {
			continue
		}
		transitions[requestID] = [2]freshRuntimeOperation{operation, next}
	}
	if len(transitions) == 0 {
		return data, nil
	}
	a.runtimeJournal.mu.Lock()
	defer a.runtimeJournal.mu.Unlock()
	latest, err := a.runtimeJournal.load(ctx)
	if err != nil {
		return data, err
	}
	applied := false
	for requestID, transition := range transitions {
		stored, found := latest.Operations[requestID]
		// Only apply a transition to the exact entry it was derived from, so a
		// concurrent submitter is never overwritten.
		if !found || stored.State != transition[0].State || !stored.UpdatedAt.Equal(transition[0].UpdatedAt) {
			continue
		}
		latest.Operations[requestID] = transition[1]
		applied = true
	}
	if applied {
		if err := a.runtimeJournal.save(ctx, latest); err != nil {
			return data, err
		}
	}
	return latest, nil
}

type freshRuntimeStatusResponse struct {
	Mode       string                       `json:"mode"`
	Status     string                       `json:"status"`
	ObservedAt *time.Time                   `json:"observedAt,omitempty"`
	Manager    freshRuntimeManagerStatus    `json:"manager"`
	Gateway    *freshRuntimeGatewayResponse `json:"gateway,omitempty"`
	LastKnown  *freshRuntimeGatewayResponse `json:"lastKnown,omitempty"`
	ErrorCode  string                       `json:"errorCode,omitempty"`
}

type freshRuntimeManagerStatus struct {
	State string `json:"state"`
}

type freshRuntimeRecoveryResponse struct {
	State             string `json:"state"`
	AttemptsRemaining int    `json:"attemptsRemaining"`
}

type freshRuntimeGatewayResponse struct {
	Identity        string `json:"identity,omitempty"`
	Generation      uint64 `json:"generation,omitempty"`
	ProtocolVersion string `json:"protocolVersion,omitempty"`
	State           string `json:"state"`
	Ready           bool   `json:"ready"`
	Version         string `json:"version,omitempty"`
	// VersionSupport and WriteBlocked are the External class of the observed
	// version: what this Manager proved about the CPA it does not own. The Embedded
	// profile leaves them empty — its artifact is the authority, not a version.
	VersionSupport string                        `json:"versionSupport,omitempty"`
	WriteBlocked   bool                          `json:"writeBlocked,omitempty"`
	ArtifactID     string                        `json:"artifactId,omitempty"`
	Capabilities   []string                      `json:"capabilities,omitempty"`
	Recovery       *freshRuntimeRecoveryResponse `json:"recovery,omitempty"`
}

func freshRuntimeGatewayStatus(status model.RuntimeObservedStatus) *freshRuntimeGatewayResponse {
	gateway := &freshRuntimeGatewayResponse{
		Identity: string(status.Identity), Generation: uint64(status.Generation),
		ProtocolVersion: string(status.ProtocolVersion), State: string(status.State),
		Ready: status.State == model.RuntimeStateReady,
	}
	gateway.Version = string(status.CPAObservedVersion)
	for _, capability := range status.Capabilities {
		gateway.Capabilities = append(gateway.Capabilities, string(capability))
	}
	if status.ActiveGatewayArtifact != nil {
		gateway.ArtifactID = string(status.ActiveGatewayArtifact.ArtifactID)
	}
	if status.Recovery != nil {
		gateway.Recovery = &freshRuntimeRecoveryResponse{
			State: string(status.Recovery.State), AttemptsRemaining: status.Recovery.AttemptsRemaining,
		}
	}
	return gateway
}

func (a *freshApplication) runtimeStatus(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation) {
	if r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !op.ProductComplete {
		w.WriteHeader(http.StatusPreconditionRequired)
		return
	}
	runtimeMode := a.runtimeModeFor(op)
	mode := string(runtimeMode)
	statusClient := a.runtimeStatusClientFor(runtimeMode)
	if statusClient == nil {
		writeFreshJSON(w, freshRuntimeUnknownResponse(mode, "runtime_unavailable", a.runtimeJournal))
		return
	}
	now := time.Now().UTC()
	var (
		observed model.RuntimeObservedStatus
		err      error
	)
	if runtimeMode == model.RuntimeModeExternal {
		var refused bool
		observed, refused, err = a.observeExternalRuntimeStatus(r.Context(), statusClient)
		if refused {
			writeFreshJSON(w, freshRuntimeUnknownResponse(mode, freshconfig.ReasonManagementRejected, a.runtimeJournal))
			return
		}
	} else {
		observed, err = statusClient.Status(r.Context())
	}
	if err != nil {
		writeFreshJSON(w, freshRuntimeUnknownResponse(mode, "runtime_unreachable", a.runtimeJournal))
		return
	}
	response := freshRuntimeStatusResponse{
		Mode: mode, Status: string(observed.State), ObservedAt: &now,
		Manager: freshRuntimeManagerStatus{State: "ready"}, Gateway: freshRuntimeGatewayStatus(observed),
	}
	if mode == string(model.RuntimeModeExternal) && response.Gateway != nil {
		// The CPA observed here belongs to the user, so the version decides what
		// this Manager may write to it. Reporting the class next to the version
		// is what lets the page say why a write is unavailable instead of
		// offering one that would be refused.
		report := cpa.ClassifyExternalVersion(string(observed.CPAObservedVersion))
		response.Gateway.VersionSupport = report.Support
		response.Gateway.WriteBlocked = report.WriteBlocked
	}
	a.runtimeJournal.setLast(&response, now)
	writeFreshJSON(w, response)
}

func freshRuntimeUnknownResponse(mode, code string, journal *freshRuntimeJournal) freshRuntimeStatusResponse {
	response := freshRuntimeStatusResponse{
		Mode: mode, Status: string(model.RuntimeStateUnknown),
		Manager: freshRuntimeManagerStatus{State: "ready"}, ErrorCode: code,
	}
	if journal == nil {
		return response
	}
	last, observedAt := journal.lastStatus()
	if last != nil {
		response.LastKnown = last.Gateway
		response.ObservedAt = &observedAt
	}
	return response
}

type freshRuntimeOperationResponse struct {
	RequestID         string    `json:"requestId"`
	Action            string    `json:"action"`
	OperationID       string    `json:"operationId"`
	State             string    `json:"state"`
	RuntimeIdentity   string    `json:"runtimeIdentity,omitempty"`
	RuntimeGeneration uint64    `json:"runtimeGeneration,omitempty"`
	FailureCode       string    `json:"failureCode,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

func (o freshRuntimeOperation) response() freshRuntimeOperationResponse {
	return freshRuntimeOperationResponse{
		RequestID: o.RequestID, Action: o.Action, OperationID: o.OperationID, State: o.State,
		RuntimeIdentity: o.RuntimeIdentity, RuntimeGeneration: o.RuntimeGeneration,
		FailureCode: o.FailureCode, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
	}
}

func (a *freshApplication) runtimeOperations(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation, resource string) {
	if r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !op.ProductComplete {
		w.WriteHeader(http.StatusPreconditionRequired)
		return
	}
	if resource == "runtime/operations" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		a.submitRuntimeOperation(w, r, op)
		return
	}
	if !strings.HasPrefix(resource, "runtime/operations/") || r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	identifier := strings.TrimPrefix(resource, "runtime/operations/")
	if !freshRuntimeOperationIDPattern.MatchString(identifier) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if a.runtimeJournal == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	data, err := a.runtimeJournal.load(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	// A page reopened after a Manager interruption must be able to learn the
	// outcome of the operation it started. Resolution only observes; this route
	// never resends the mutation.
	if a.runtimeModeFor(op) == model.RuntimeModeEmbedded && freshRuntimeOperationsPending(data) {
		if resolved, resolveErr := a.refreshRuntimeOperations(r.Context(), nil); resolveErr == nil {
			data = resolved
		}
	}
	operation, found := data.Operations[identifier]
	if !found {
		for _, candidate := range data.Operations {
			if candidate.OperationID == identifier {
				operation, found = candidate, true
				break
			}
		}
	}
	if !found {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	writeFreshJSON(w, operation.response())
}

func (a *freshApplication) submitRuntimeOperation(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation) {
	var request struct {
		Action    string `json:"action"`
		RequestID string `json:"requestId"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || !freshRuntimeRequestIDPattern.MatchString(request.RequestID) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if request.Action != string(model.RuntimeOperationStart) && request.Action != string(model.RuntimeOperationStop) && request.Action != string(model.RuntimeOperationRestart) {
		freshRuntimeOperationError(w, http.StatusBadRequest, "invalid_action")
		return
	}
	if a.runtimeModeFor(op) != model.RuntimeModeEmbedded || a.effectiveRuntimeMode(r.Context()) != model.RuntimeModeEmbedded || a.embeddedRuntimeClient() == nil {
		freshRuntimeOperationError(w, http.StatusConflict, "runtime_mutation_unsupported")
		return
	}
	if a.runtimeJournal == nil {
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "operation_journal_unavailable")
		return
	}
	if a.runtimeSettings == nil {
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "desired_lifecycle_unavailable")
		return
	}
	// Acquire the shared lifecycle lock on the caller's HTTP context before
	// observing Status, reserving an accepted journal row, or starting the 40s
	// mutation budget. A request cancelled while an automatic reconcile holds
	// the lock exits cleanly without leaving a non-terminal journal blocker, and
	// once acquired the submission observes post-reconcile Status and receives
	// its full mutation budget.
	unlockLifecycle, err := a.runtimeLifecycleMu.Lock(r.Context())
	if err != nil {
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "runtime_unreachable")
		return
	}
	defer unlockLifecycle()
	// The desired lifecycle is the Runtime domain's own durable record. Unreadable
	// bytes there lock only that domain, and the lock is decided before any
	// Supervisor call: a mutation whose follow-up write can never be read back is
	// refused instead of being performed and then reported as unknown.
	if _, _, err := a.runtimeSettings.LoadEmbeddedRuntimeDesiredState(r.Context()); err != nil {
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "desired_lifecycle_unavailable")
		return
	}
	// One observation fences the submission and resolves abandoned journal
	// entries, so no second Runtime call is needed.
	observed, err := a.embeddedRuntimeClient().Status(r.Context())
	if err != nil {
		if errors.Is(err, errEmbeddedRuntimeDisabled) || a.effectiveRuntimeMode(context.WithoutCancel(r.Context())) == model.RuntimeModeExternal {
			freshRuntimeOperationError(w, http.StatusConflict, "runtime_mutation_unsupported")
			return
		}
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "runtime_unreachable")
		return
	}
	if observed.State == model.RuntimeStateUnknown {
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "runtime_observation_unknown")
		return
	}
	operationType := model.RuntimeOperationType(request.Action)
	if !observed.Capabilities.Supports(model.RuntimeCapability(request.Action)) {
		freshRuntimeOperationError(w, http.StatusConflict, "capability_not_supported")
		return
	}
	// Entries that can no longer be confirmed must not keep blocking a different
	// action, so the journal is resolved before the conflict gate.
	if _, err := a.refreshRuntimeOperations(r.Context(), &observed); err != nil {
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "operation_journal_unavailable")
		return
	}
	now := time.Now().UTC()
	reserved := freshRuntimeOperation{
		RequestID: request.RequestID, Action: request.Action,
		OperationID:     "runtime-ui/v1:" + request.RequestID,
		State:           freshRuntimeStateAccepted,
		RuntimeIdentity: string(observed.Identity), RuntimeGeneration: uint64(observed.Generation),
		CreatedAt: now, UpdatedAt: now,
	}
	if a.effectiveRuntimeMode(r.Context()) != model.RuntimeModeEmbedded {
		freshRuntimeOperationError(w, http.StatusConflict, "runtime_mutation_unsupported")
		return
	}
	replayed, code := a.reserveRuntimeOperation(r.Context(), reserved)
	switch code {
	case "":
	case "operation_journal_unavailable":
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, code)
		return
	default:
		freshRuntimeOperationError(w, http.StatusConflict, code)
		return
	}
	if replayed != nil {
		writeFreshJSON(w, replayed.response())
		return
	}
	var expectedArtifact model.RuntimeArtifactID
	if operationType == model.RuntimeOperationStart && observed.ActiveGatewayArtifact != nil {
		expectedArtifact = observed.ActiveGatewayArtifact.ArtifactID
	}
	// Detach the mutation and its durable follow-up from HTTP cancellation. The
	// explicit budget leaves accepted Supervisor work bounded while giving the
	// desired lifecycle and terminal journal writes time to complete.
	mutationContext, cancelMutation := context.WithTimeout(
		context.WithoutCancel(r.Context()), freshRuntimeMutationBudget,
	)
	defer cancelMutation()
	result, mutationErr := a.submitRuntimeMutation(mutationContext, operationType, model.RuntimeMutationRequest{
		OperationID: reserved.OperationID, ExpectedRuntimeIdentity: observed.Identity,
		ExpectedRuntimeGeneration: observed.Generation, ExpectedActiveArtifactID: expectedArtifact,
	})
	outcome := reserved
	outcome.UpdatedAt = time.Now().UTC()
	conflictCode := runtimeMutationConflictCode(mutationErr)
	switch {
	case conflictCode != "":
		// The Supervisor rejected the mutation before any side effect. The entry
		// becomes terminal and the desired lifecycle is never written, so the
		// reconciler cannot drive the Runtime toward a state the user's operation
		// did not reach.
		outcome.State = freshRuntimeStateFailed
		outcome.FailureCode = conflictCode
	case mutationErr == nil && result.Validate() == nil:
		outcome.State = string(result.State)
		if result.Failure != nil {
			outcome.FailureCode = result.Failure.Code
		}
	default:
		// The response was lost after the Supervisor may have accepted the
		// mutation. The desired lifecycle is still adopted so the reconciler
		// converges on what the user asked for, and the entry stays resolvable.
		outcome.State = freshRuntimeStateUnknown
		outcome.FailureCode = "runtime_result_unknown"
	}
	desiredWriteFailed := false
	// Only accepted, running, succeeded, or unknown results may adopt the
	// requested lifecycle. A valid Supervisor failed result proves the
	// operation did not reach that lifecycle, so leave the reconciler's desired
	// state unchanged.
	if conflictCode == "" && outcome.State != freshRuntimeStateFailed {
		if err := a.applyRuntimeDesiredLifecycle(mutationContext, operationType); err != nil {
			// The Runtime side effect may already have happened, but without the
			// durable desired lifecycle the reconciler cannot converge on it.
			desiredWriteFailed = true
			outcome.State = freshRuntimeStateFailed
			outcome.FailureCode = "desired_lifecycle_unavailable"
		}
	}
	if err := a.finalizeRuntimeOperation(mutationContext, outcome); err != nil {
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "operation_journal_unavailable")
		return
	}
	if desiredWriteFailed {
		freshRuntimeOperationError(w, http.StatusServiceUnavailable, "desired_lifecycle_unavailable")
		return
	}
	if conflictCode != "" {
		// Preserve the 409 contract for a Supervisor rejection while the durable
		// entry already records the terminal outcome.
		freshRuntimeOperationError(w, http.StatusConflict, conflictCode)
		return
	}
	if outcome.State == freshRuntimeStateUnknown {
		w.WriteHeader(http.StatusAccepted)
	}
	writeFreshJSON(w, outcome.response())
}

// reserveRuntimeOperation durably records the in-flight operation while holding
// the journal mutex. It returns the previously recorded entry for an idempotent
// replay, or a stable error code when the action is refused.
func (a *freshApplication) reserveRuntimeOperation(ctx context.Context, operation freshRuntimeOperation) (*freshRuntimeOperation, string) {
	a.runtimeJournal.mu.Lock()
	defer a.runtimeJournal.mu.Unlock()
	data, err := a.runtimeJournal.load(ctx)
	if err != nil {
		return nil, "operation_journal_unavailable"
	}
	if existing, found := data.Operations[operation.RequestID]; found {
		if existing.Action != operation.Action {
			return nil, "operation_id_conflict"
		}
		previous := existing
		return &previous, ""
	}
	if freshRuntimeOperationBlocks(data, operation) {
		return nil, "operation_conflict"
	}
	data.Operations[operation.RequestID] = operation
	if err := a.runtimeJournal.save(ctx, data); err != nil {
		return nil, "operation_journal_unavailable"
	}
	return nil, ""
}

// finalizeRuntimeOperation persists the outcome of an operation this request
// reserved. An entry already made terminal elsewhere is retained as-is.
func (a *freshApplication) finalizeRuntimeOperation(ctx context.Context, operation freshRuntimeOperation) error {
	a.runtimeJournal.mu.Lock()
	defer a.runtimeJournal.mu.Unlock()
	data, err := a.runtimeJournal.load(ctx)
	if err != nil {
		return err
	}
	if stored, found := data.Operations[operation.RequestID]; found && freshRuntimeOperationTerminal(stored) {
		return nil
	}
	data.Operations[operation.RequestID] = operation
	return a.runtimeJournal.save(ctx, data)
}

func (a *freshApplication) submitRuntimeMutation(
	ctx context.Context,
	operationType model.RuntimeOperationType,
	request model.RuntimeMutationRequest,
) (model.RuntimeOperationResult, error) {
	client := a.embeddedRuntimeClient()
	if client == nil {
		return model.RuntimeOperationResult{}, errEmbeddedRuntimeDisabled
	}
	switch operationType {
	case model.RuntimeOperationStart:
		return client.Start(ctx, request)
	case model.RuntimeOperationStop:
		return client.Stop(ctx, request)
	case model.RuntimeOperationRestart:
		return client.Restart(ctx, request)
	}
	return model.RuntimeOperationResult{}, errors.New("unsupported runtime lifecycle operation")
}

// applyRuntimeDesiredLifecycle is the single Manager-owned writer of the
// desired lifecycle for a submitted operation. It is never called for a rejected
// mutation, so a Supervisor conflict cannot leave the lifecycle flipped.
func (a *freshApplication) applyRuntimeDesiredLifecycle(ctx context.Context, operationType model.RuntimeOperationType) error {
	if a.effectiveRuntimeMode(ctx) != model.RuntimeModeEmbedded {
		return errEmbeddedRuntimeDisabled
	}
	desired := model.EmbeddedRuntimeDesiredRunning
	if operationType == model.RuntimeOperationStop {
		desired = model.EmbeddedRuntimeDesiredStopped
	}
	_, err := a.runtimeSettings.SetEmbeddedRuntimeDesiredLifecycle(ctx, desired)
	return err
}

// runtimeMutationConflictCode maps the Supervisor rejections that guarantee no
// side effect. Every other error leaves the result unknown.
func runtimeMutationConflictCode(err error) string {
	if errors.Is(err, errEmbeddedRuntimeDisabled) {
		return "runtime_mutation_unsupported"
	}
	code, ok := runtimeservice.ErrorCode(err)
	if !ok {
		return ""
	}
	switch code {
	case runtimeservice.ProtocolErrorOperationIDConflict:
		return "operation_id_conflict"
	case runtimeservice.ProtocolErrorOperationStateConflict:
		return "operation_conflict"
	}
	return ""
}

func freshRuntimeOperationError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeFreshJSON(w, struct {
		Code string `json:"code"`
	}{Code: code})
}

type freshStoreView struct {
	app *freshApplication
}

func (s freshStoreView) Load(ctx context.Context) (freshstore.Record, error) {
	if s.app == nil || s.app.store == nil {
		return freshstore.Record{}, freshstore.ErrNotFresh
	}
	return s.app.store.Load(ctx)
}

func (s freshStoreView) Create(ctx context.Context, id bootstrap.FreshIdentity, admin *model.AdminCredential) (freshstore.Record, error) {
	if s.app == nil || s.app.store == nil {
		return freshstore.Record{}, freshstore.ErrNotFresh
	}
	return s.app.store.Create(ctx, id, admin)
}

func (s freshStoreView) Update(ctx context.Context, id bootstrap.FreshIdentity, rev uint64, fn func(*freshstore.Record) error) (freshstore.Record, error) {
	if s.app == nil || s.app.store == nil {
		return freshstore.Record{}, freshstore.ErrNotFresh
	}
	return s.app.store.Update(ctx, id, rev, fn)
}

type freshEmbeddedDesiredStateStore struct {
	app   *freshApplication
	inner runtimeservice.DesiredStateStore
}

func (a *freshApplication) embeddedDesiredStateInner() runtimeservice.DesiredStateStore {
	if a == nil {
		return nil
	}
	if a.desiredState != nil {
		return a.desiredState
	}
	return a.runtimeSettings
}

func (s freshEmbeddedDesiredStateStore) LoadEmbeddedRuntimeDesiredState(ctx context.Context) (model.EmbeddedRuntimeDesiredState, bool, error) {
	if s.app == nil || s.inner == nil {
		return model.EmbeddedRuntimeDesiredState{}, false, nil
	}
	if s.app.store != nil {
		op, err := s.app.current(ctx)
		if err != nil {
			return model.EmbeddedRuntimeDesiredState{}, false, err
		}
		if op.Strategy.Strategy == bootstrap.ConnectExternal {
			s.app.markEmbeddedExecutionClosed()
			return model.EmbeddedRuntimeDesiredState{}, false, nil
		}
	}
	return s.inner.LoadEmbeddedRuntimeDesiredState(ctx)
}

func (s freshEmbeddedDesiredStateStore) LoadEmbeddedRuntimeCommittedArtifact(ctx context.Context) (model.RuntimeArtifactID, bool, error) {
	if s.app == nil || s.inner == nil {
		return "", false, nil
	}
	if s.app.store != nil {
		op, err := s.app.current(ctx)
		if err != nil {
			return "", false, err
		}
		if op.Strategy.Strategy == bootstrap.ConnectExternal {
			s.app.markEmbeddedExecutionClosed()
			return "", false, nil
		}
	}
	committed, ok := s.inner.(interface {
		LoadEmbeddedRuntimeCommittedArtifact(context.Context) (model.RuntimeArtifactID, bool, error)
	})
	if !ok {
		return "", false, nil
	}
	return committed.LoadEmbeddedRuntimeCommittedArtifact(ctx)
}

// runFreshEmbeddedReconciler keeps the Embedded desired-lifecycle reconciler
// active before strategy selection and throughout New Managed / Adopt Existing,
// and retires it permanently once Connect External is committed in-process.
func (a *freshApplication) runFreshEmbeddedReconciler(ctx context.Context, interval time.Duration) {
	desired := a.embeddedDesiredStateInner()
	client := a.embeddedRuntimeClient()
	if a == nil || client == nil || desired == nil {
		return
	}
	if op, err := a.current(ctx); err == nil && op.Strategy.Strategy == bootstrap.ConnectExternal {
		a.markEmbeddedExecutionClosed()
		return
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	gate := &a.embeddedExecution
	gate.mu.Lock()
	if gate.closed {
		gate.mu.Unlock()
		cancel()
		close(done)
		return
	}
	gate.reconcilerCancel = cancel
	gate.reconcilerDone = done
	gate.mu.Unlock()
	defer func() {
		cancel()
		gate.mu.Lock()
		if gate.reconcilerDone == done {
			gate.reconcilerCancel = nil
			gate.reconcilerDone = nil
		}
		gate.mu.Unlock()
	}()
	reconciler := runtimeservice.NewReconciler(
		client,
		freshEmbeddedDesiredStateStore{app: a, inner: desired},
		interval,
		log.Printf,
	)
	go func() {
		defer close(done)
		reconciler.Run(workerCtx)
	}()
	externalCommitted := a.externalCommittedSignal()
	watchInterval := interval
	if watchInterval < time.Second {
		watchInterval = time.Second
	}
	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			<-done
			return
		case <-done:
			return
		case <-externalCommitted:
			cancel()
			<-done
			return
		case <-ticker.C:
			op, err := a.current(ctx)
			if err == nil && op.Strategy.Strategy == bootstrap.ConnectExternal {
				a.markEmbeddedExecutionClosed()
				<-done
				return
			}
		}
	}
}
