package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshadopt"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
)

// adoptMigrationSourceClass is the only source class the v8 migration executor
// can read. A legacy_managed source is refused before this point, but the check
// is repeated here so the executor is never pointed at a process whose
// management surface is not v8.
const adoptMigrationSourceClass = bootstrap.AdoptNetworkExisting

// adoptMigrationDeadline bounds one whole background run. The commit reply does
// not wait for it, so this is the only thing that stops a source which answers
// slowly forever: the run is cancelled, whatever it recorded stays recorded, and
// the items it never reached are left unrecorded so a later replay retries them.
const adoptMigrationDeadline = 10 * time.Minute

// adoptMigrationStateRunning is the migration state the commit reply reports.
// The reply no longer waits for the copy, so it says exactly what is true at the
// moment it is written: the run has started, and its progress is the per-item
// summary the record already carries.
const adoptMigrationStateRunning = "running"

// adoptMigrationSession re-checks the installation session before every
// checkpoint. A migration outlives the request that started it, so the authority
// that authorized the commit is re-read rather than carried: a revoked or
// expired continuation stops the copy at the next item instead of letting it
// write under an authority that no longer exists.
type adoptMigrationSession func(record freshstore.Record) error

// runAdoptionMigration copies the bounded inventory from the source CPA to the
// destination this operation has just committed, checkpointing one result per
// item into the Fresh record.
//
// This runs only after the destination is committed and its client keys are
// verified, so every write lands on an installation that already owns its
// identity. It is started again on a committed replay, which is what makes the
// migration resumable: the executor skips every item the record already
// settled, and an item whose write result was unknown is reconciled by reading
// the destination back rather than by sending the write again.
//
// Nothing here can fail the adoption. The destination is already committed and
// verified; a manual item, a failed item and an unreachable source are all
// reported in the summary for the completion screen (Step 4 reads it), and a
// summary that could not be written leaves the run resumable from the items it
// did record.
func (a *freshApplication) runAdoptionMigration(ctx context.Context, record freshstore.Record, sourceBaseURL, sourceManagementKey, sessionToken string) string {
	if record.Connection == nil || a.protector == nil || record.Operation.Strategy.Strategy != bootstrap.AdoptExisting ||
		record.Operation.Strategy.SourceClass != adoptMigrationSourceClass || record.Operation.Strategy.Stage != "committed" {
		return ""
	}
	if record.Operation.ProductComplete {
		// The installation is finished. Copying into it now would overwrite
		// choices the operator made on the destination after completion, and the
		// source is by definition a different install: the migration is over.
		return ""
	}
	destinationManagementKey, err := a.protector.UnprotectString(record.Connection.ManagementKey)
	if err != nil {
		// The committed destination credential is unreadable, so no destination
		// write is possible. The adoption itself stays committed and complete.
		log.Printf("Fresh adoption migration skipped: destination credential unavailable")
		return ""
	}
	identity := record.Operation.Identity
	return a.startAdoptionMigration(ctx, identity, func(ctx context.Context, current freshstore.Record) {
		previous := freshstore.AdoptionSummary{}
		if current.AdoptionSummary != nil {
			previous = *current.AdoptionSummary
		}
		// The run identity was assigned and persisted under the start lock before
		// this function ran, so the record carries the generation every checkpoint
		// of this run must still find. A record without one is not a run this
		// build started.
		if previous.RunID == "" {
			log.Printf("Fresh adoption migration skipped: run generation unavailable")
			return
		}
		// Both clients fix their DNS resolution once and are closed with the run.
		// A source or destination that cannot be reached ends the run here; the
		// adoption itself is already committed.
		source, err := freshadopt.NewSource(ctx, freshadopt.Endpoint{BaseURL: sourceBaseURL, ManagementKey: sourceManagementKey})
		if err != nil {
			log.Printf("Fresh adoption migration skipped: source endpoint invalid")
			return
		}
		defer source.Close()
		destination, err := freshadopt.NewDestination(ctx, freshadopt.Endpoint{BaseURL: current.Connection.CPABaseURL, ManagementKey: destinationManagementKey})
		if err != nil {
			log.Printf("Fresh adoption migration skipped: destination endpoint invalid")
			return
		}
		defer destination.Close()
		// The checkpoint is built from the run's own context, not the request's:
		// the reply has already returned, and every write this run makes has to
		// outlive it.
		checkpoint := persistAdoptionSummary(ctx, a, identity, previous.RunID, a.adoptionMigrationSession(ctx, sessionToken))
		result, runErr := freshadopt.New(source, destination, current.Operation.Strategy.AdoptionDecisions).Run(ctx, previous, checkpoint)
		if runErr != nil {
			// Only the bounded code is logged. A runner error can quote an upstream
			// value, and the code is the whole diagnosis the log needs.
			log.Printf("Fresh adoption migration stopped after %d items: %v", len(result.Summary.Items), adoptionMigrationCode(runErr))
			return
		}
		migrated, manual, failed := result.Summary.Counts()
		log.Printf("Fresh adoption migration recorded %d migrated, %d manual, %d failed items", migrated, manual, failed)
	})
}

// startAdoptionMigration starts one background run and reports the migration
// state the caller may reply with: `running` when this call started the run or a
// run was already in flight, and an empty state when nothing was started.
//
// It is the single-flight lock of the migration: the re-read, the check and the
// start happen under one mutex, so two concurrent committed replays cannot both
// begin a copy, and the flag is cleared only when the run has finished so a later
// replay can resume a run that stopped.
//
// The record the caller derived this start from was read before the lock, which
// is exactly why it is not trusted here: a completion that held the lock while
// this call waited has set `ProductComplete`, and a run started on the stale
// record would write into a finished installation after the operator's own
// changes. The record is therefore re-read and re-validated under the lock —
// still this adoption, still committed, not complete — and the run receives that
// fresh record rather than the one the caller held.
//
// The run is derived from the host lifetime, not from the request: the reply
// returns at once, and the request that started the copy ends long before it
// does. The host lifetime is the right parent because the copy writes into this
// process's installation, so a host shutdown must cancel it rather than leave a
// goroutine writing into a closing database; the deadline bounds it besides.
//
// Every actual run start writes a fresh run identity through the record's own
// revision CAS while this lock is held, before any network work and before the
// caller is told `running`. The generation is what makes an acknowledgement of
// one run's retryable items inapplicable to another run whose items look the
// same. A replay that finds a run already in flight starts nothing and reuses
// the generation that run wrote; it never burns an epoch.
func (a *freshApplication) startAdoptionMigration(ctx context.Context, identity bootstrap.FreshIdentity, run func(context.Context, freshstore.Record)) string {
	a.adoptMigrationMu.Lock()
	if a.adoptMigrationRunning {
		a.adoptMigrationMu.Unlock()
		return adoptMigrationStateRunning
	}
	current, started := a.beginAdoptionRun(ctx, identity)
	if !started {
		// Either this record is no longer the committed, unfinished adoption the
		// replay describes, or its last run already settled with nothing left to
		// copy. Neither gets a run — the first would write into an installation the
		// replay no longer describes, the second would reopen a settled run — and
		// the replay is answered from the durable record as it stands.
		a.adoptMigrationMu.Unlock()
		return ""
	}
	basis := a.runCtx
	if basis == nil {
		basis = context.Background()
	}
	a.adoptMigrationRunning = true
	deadline, cancel := context.WithTimeout(basis, adoptMigrationDeadline)
	a.adoptMigrationMu.Unlock()
	go func() {
		defer cancel()
		defer func() {
			a.adoptMigrationMu.Lock()
			a.adoptMigrationRunning = false
			a.adoptMigrationMu.Unlock()
		}()
		run(deadline, current)
	}()
	return adoptMigrationStateRunning
}

// beginAdoptionRun assigns and persists the identity of one new run. The caller
// holds `adoptMigrationMu`, so no other run can take the record between the
// re-read, the check and the CAS; the revision CAS keeps store-level writers
// honest besides.
//
// The write carries the items and plan the record already held: a resume
// continues from the durable prefix rather than discarding it, and `Finished` is
// cleared because the new run has not processed its plan yet. A conflict is
// another writer having moved the record, which the next attempt resolves; a
// conflict that survives the retry bound is a record this run must not touch.
//
// A record whose last run already settled — finished, every planned item
// recorded, no retryable item left — starts nothing. Reopening it would burn a
// generation and clear `Finished`, so an operator who replays the committed
// strategy before completing the installation could leave a complete-able
// installation behind if the source has since become unreachable. The replay is
// answered with the durable record as it stands, and the settled generation is
// kept; only genuine work (an unfinished or retryable run) gets a new one.
func (a *freshApplication) beginAdoptionRun(ctx context.Context, identity bootstrap.FreshIdentity) (freshstore.Record, bool) {
	for range adoptSummaryWriteAttempts {
		current, err := a.store.Load(ctx)
		if err != nil || current.Operation.Identity != identity || !adoptionMigrationCommitted(current) {
			return freshstore.Record{}, false
		}
		if current.AdoptionSummary != nil && adoptionMigrationSettled(*current.AdoptionSummary) {
			return current, false
		}
		runID, err := newAdoptionRunID()
		if err != nil {
			return freshstore.Record{}, false
		}
		next, err := a.store.Update(ctx, identity, current.Operation.Revision, func(record *freshstore.Record) error {
			if record.Operation.Identity != identity || !adoptionMigrationCommitted(*record) {
				return freshstore.ErrConflict
			}
			summary := freshstore.AdoptionSummary{}
			if record.AdoptionSummary != nil {
				summary = *record.AdoptionSummary
			}
			summary.RunID, summary.Finished = runID, false
			record.AdoptionSummary = &summary
			return nil
		})
		if err == nil {
			return next, true
		}
		if !errors.Is(err, freshstore.ErrConflict) {
			return freshstore.Record{}, false
		}
	}
	return freshstore.Record{}, false
}

// newAdoptionRunID returns one opaque 128-bit run identity in the lower-case hex
// the record's own validator accepts. It is random, not derived from the
// installation or the time: two runs on one installation must never be
// guessable from each other, because the identity is what binds an operator's
// acknowledgement to the run they were shown.
func newAdoptionRunID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// adoptMigrationInFlight reports whether a background copy is running right now.
// It is the read half of the single-flight lock, so the completion path and the
// start path agree on what "running" means. A caller that also decides something
// under the lock takes `adoptMigrationMu` itself instead.
func (a *freshApplication) adoptMigrationInFlight() bool {
	a.adoptMigrationMu.Lock()
	defer a.adoptMigrationMu.Unlock()
	return a.adoptMigrationRunning
}

// adoptMigrationRunChanged refuses an acknowledgement of retryable items that
// was given for a different run than the durable record's current one. The
// operator is asked to read the run they are looking at before accepting it; the
// refusal is a code, not a generic conflict, because the page has to clear the
// stale acknowledgement rather than retry the same request.
const adoptMigrationRunChanged = "adopt_migration_run_changed"

// errAdoptMigrationRunChanged carries that refusal out of the completion's own
// CAS, where the durable run identity — not the record the handler read — is the
// authority.
var errAdoptMigrationRunChanged = errors.New("adopt migration run changed")

// adoptCompleteRequest is the only body a completion request may carry. An
// adoption's conditions are the record's, so the body never states them; the run
// identity is not a condition either, it is the binding of the operator's
// acknowledgement to the run they were shown.
type adoptCompleteRequest struct {
	// AcceptRetryable lets the operator finish over items a run could not read
	// from the source and wrote nothing for. It never accepts an item whose write
	// result is unknown: that one's only resolution is its own read-back.
	AcceptRetryable bool `json:"acceptRetryable,omitempty"`
	// RunID is the run the acknowledgement belongs to. It is required whenever
	// AcceptRetryable is set, and must equal the durable summary's current run
	// identity under the completion's own CAS: two runs may leave the same
	// retryable item ids and counts, and the identity is the only thing that tells
	// the operator's confirmation apart.
	RunID string `json:"runId,omitempty"`
}

// adoptComplete finishes an adopted installation once its migration has really
// processed every planned item.
//
// The checks run in this order and each one refuses with its own answer:
//
//  1. a copy is not running right now;
//  2. the installation is still committed;
//  3. the protected connection decrypts and the destination passes the Adopt
//     posture — the real proof that this Manager owns the CPA it completes;
//  4. the destination carries at least one client key, because a CPA with none
//     accepts unauthenticated Gateway traffic;
//  5. the run recorded `Finished` with every planned item present;
//  6. no item is retryable, unless the request accepts them;
//  7. an accepted set of retryable items names the durable run's current
//     generation, verified again in the completion's own CAS.
//
// Only once all of them hold are the destination's client keys registered in the
// Key workspace and the install journal claimed for this run: that is the write
// that makes the adopted installation manageable afterwards, and it must not
// happen for a migration that is not finished. A destination list this
// installation cannot represent — a key it already retired, a journal another
// installation owns — is answered with `adopt_installation_conflict`, because no
// retry changes those facts; a storage failure keeps the retryable
// `adopt_installation_unavailable`.
//
// Every one of them is decided while `adoptMigrationMu` is held, and the CAS that
// sets `ProductComplete` runs under the same lock, so a run can never start
// between the last check and the write. The lock is released before the reply is
// written and before the usage startup runs: that startup samples the destination
// over the network, and a migration run must not wait on a sample to take the
// same lock.
func (a *freshApplication) adoptComplete(w http.ResponseWriter, r *http.Request, op bootstrap.FreshOperation, record freshstore.Record, continuation string, freshAuth bool) {
	var request adoptCompleteRequest
	// An empty body is the ordinary call; any body is bounded and closed.
	if r.Body != nil {
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		d.DisallowUnknownFields()
		if err := d.Decode(&request); err != nil && !errors.Is(err, io.EOF) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	if op.ProductComplete {
		// An already-completed adoption replays its registration proof so an
		// installation completed on an earlier build obtains its adopted key
		// records and one-shot install journal once under the same read-only
		// destination checks, while a conflict or outage fails closed instead of
		// answering 200 silently.
		decision := a.reconcileCompletedAdoption(r.Context())
		if decision.code != "" {
			writeFreshAdoptCompleteError(w, decision.status, decision.code, decision.retryable)
			return
		}
		if decision.status != 0 {
			w.WriteHeader(decision.status)
			return
		}
		a.startUsageCollection(op)
		writeFreshJSON(w, op)
		return
	}
	authority := a.keyApply.ForOperation(op)
	if op.Strategy.Stage != "committed" || record.Connection == nil || authority == nil ||
		authority.Profile() != freshkeyapply.ProfileManaged ||
		!freshkeyapply.ValidManagerOwnedDestinationCheckpoint(op.Strategy) {
		w.WriteHeader(http.StatusConflict)
		return
	}
	if a.protector == nil {
		writeFreshAdoptCompleteError(w, http.StatusServiceUnavailable, "adopt_installation_unavailable", 0)
		return
	}
	managementKey, err := a.protector.UnprotectString(record.Connection.ManagementKey)
	if err != nil {
		writeFreshAdoptCompleteError(w, http.StatusServiceUnavailable, "adopt_installation_unavailable", 0)
		return
	}
	a.adoptMigrationMu.Lock()
	decision := a.decideAdoptionCompletion(r.Context(), op, managementKey, continuation, freshAuth, request)
	a.adoptMigrationMu.Unlock()
	if decision.code != "" {
		writeFreshAdoptCompleteError(w, decision.status, decision.code, decision.retryable)
		return
	}
	if decision.status != 0 {
		w.WriteHeader(decision.status)
		return
	}
	if _, err := a.ensureManagedRuntimeDesiredRunning(r.Context(), decision.next.Operation); err != nil {
		log.Printf("Fresh managed runtime desired state unavailable: %v", err)
	}
	// The gateway reported no plugin support while completion was pending, and
	// that answer may be cached; the completed install has to be probed afresh.
	a.pluginCapability.reprove(r.Context())
	// Completion is the moment the install-derived CPA authority exists, so the
	// same idempotent startup path as a resumed install runs now.
	a.startUsageCollection(decision.next.Operation)
	writeFreshJSON(w, decision.next.Operation)
}

// adoptCompletion is what the completion decided under the migration lock: the
// completed record, or the refusal the operator acts on. Exactly one of the two
// is populated — a zero status with a code is a coded refusal, a non-zero status
// without a code is a bare HTTP refusal.
type adoptCompletion struct {
	next      freshstore.Record
	status    int
	code      string
	retryable int
}

// decideAdoptionCompletion runs the completion's checks and its CAS. The caller
// holds `adoptMigrationMu` and releases it before using the result: nothing here
// may hold the lock across a network sample other than the proof itself, which
// has to be decided with the run's single-flight flag.
//
// The record the handler read is not trusted: it was read before this lock, and a
// run start takes the same lock. Everything below is decided on the durable record
// as it is now, and the CAS that completes the installation uses the revision
// that read returned.
func (a *freshApplication) decideAdoptionCompletion(ctx context.Context, op bootstrap.FreshOperation, managementKey, continuation string, freshAuth bool, request adoptCompleteRequest) adoptCompletion {
	if a.adoptMigrationRunning {
		// The operator retries once the copy has settled. Nothing is written.
		return adoptCompletion{status: http.StatusConflict, code: "adopt_migration_running"}
	}
	current, err := a.store.Load(ctx)
	if err != nil {
		return adoptCompletion{status: http.StatusServiceUnavailable, code: "adopt_installation_unavailable"}
	}
	if current.Operation.Identity != op.Identity {
		return adoptCompletion{status: http.StatusConflict}
	}
	if freshAuth {
		if err := a.authority.ValidateContinuation(current, continuation); err != nil {
			return adoptCompletion{status: http.StatusConflict}
		}
	}
	// The shape the CAS insists on is decided here first, before anything is
	// written. A registration that outlived a checkpoint this completion may no
	// longer finish would hand the gateway configuration page and the key journal
	// authority over an installation that is not (or is no longer) this adoption.
	if current.Operation.Strategy.Stage != "committed" || current.Operation.Strategy.Strategy != bootstrap.AdoptExisting || current.Operation.ProductComplete {
		return adoptCompletion{status: http.StatusConflict}
	}
	record := current
	summary := freshstore.AdoptionSummary{}
	if record.AdoptionSummary != nil {
		summary = *record.AdoptionSummary
	}
	// An acknowledgement belongs to the run the operator was shown. The same
	// comparison is repeated inside the CAS below, where the write itself is
	// decided.
	if request.AcceptRetryable && (request.RunID == "" || summary.RunID != request.RunID) {
		return adoptCompletion{status: http.StatusConflict, code: adoptMigrationRunChanged}
	}
	// One authenticated read of the destination is conditions 3 and 4: the Adopt
	// posture proves the key this Manager installed, and the snapshot carries the
	// client keys the destination actually has.
	destinationKeys, err := a.keyApply.AdoptedDestinationKeys(ctx, managementKey)
	if err != nil {
		return adoptCompletion{status: http.StatusServiceUnavailable, code: "adopt_destination_unproven"}
	}
	if !summary.Finished || len(summary.Items) < summary.Planned {
		return adoptCompletion{status: http.StatusConflict, code: "adopt_migration_incomplete"}
	}
	retryable := 0
	for _, item := range summary.Items {
		if summary.Retryable(item.Item) {
			retryable++
		}
	}
	if retryable > 0 && !request.AcceptRetryable {
		return adoptCompletion{status: http.StatusConflict, code: "adopt_migration_retryable", retryable: retryable}
	}
	// Completion commits the destination's Client Keys, the one-shot install
	// journal and `ProductComplete` in one immediate SQLite transaction. Any
	// conflict (a retired key, a foreign journal, a stale continuation or a
	// moved revision) or storage failure rolls back the whole transaction so no
	// incomplete adoption ever exposes adopted keys or write authority.
	prepared, err := a.keyApply.PrepareAdoption(ctx, destinationKeys)
	if err != nil {
		if errors.Is(err, freshkeyapply.ErrAdoptionConflict) || errors.Is(err, freshkeyapply.ErrGeneration) {
			return adoptCompletion{status: http.StatusConflict, code: "adopt_installation_conflict"}
		}
		return adoptCompletion{status: http.StatusServiceUnavailable, code: "adopt_installation_unavailable"}
	}
	updater, ok := a.store.(freshstore.TransactionalUpdater)
	if !ok {
		return adoptCompletion{status: http.StatusServiceUnavailable, code: "adopt_installation_unavailable"}
	}
	next, err := updater.UpdateTx(ctx, op.Identity, record.Operation.Revision, func(record *freshstore.Record, tx *sql.Tx) error {
		if freshAuth {
			if err := a.authority.ValidateContinuation(*record, continuation); err != nil {
				return err
			}
		}
		if record.Operation.Strategy.Stage != "committed" || record.Operation.Strategy.Strategy != bootstrap.AdoptExisting || record.Operation.ProductComplete {
			return freshstore.ErrConflict
		}
		if request.AcceptRetryable && (record.AdoptionSummary == nil || record.AdoptionSummary.RunID != request.RunID) {
			return errAdoptMigrationRunChanged
		}
		if freshkeyapply.ManagedRunID(string(record.Operation.Identity.OperationID)) != prepared.RunID() ||
			strconv.FormatUint(record.Operation.Strategy.TargetGeneration, 10) != prepared.Generation() {
			return freshstore.ErrConflict
		}
		if _, err := a.keyApply.CommitAdoptionTx(ctx, tx, prepared, record.Operation.Strategy.TargetIdentity); err != nil {
			return err
		}
		record.Operation.ProductComplete = true
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, errAdoptMigrationRunChanged):
			return adoptCompletion{status: http.StatusConflict, code: adoptMigrationRunChanged}
		case errors.Is(err, freshkeyapply.ErrAdoptionConflict), errors.Is(err, freshkeyapply.ErrGeneration):
			return adoptCompletion{status: http.StatusConflict, code: "adopt_installation_conflict"}
		case errors.Is(err, freshstore.ErrConflict), errors.Is(err, freshstore.ErrNotFresh), errors.Is(err, fresh.ErrAuthority):
			return adoptCompletion{status: http.StatusConflict}
		default:
			return adoptCompletion{status: http.StatusServiceUnavailable, code: "adopt_installation_unavailable"}
		}
	}
	return adoptCompletion{next: next}
}

// reconcileCompletedAdoption ensures a completed Managed Adopt installation
// holds its adopted Client Keys, source bindings and one-shot install journal.
// Normal post-PR completions already satisfy AdoptionRegistrationReady in one
// local SQLite read and return immediately without network or write work; an
// upgraded installation completed before journaled adoption existed proves its
// live Runtime and destination CPA read-only and commits the registration in
// one immediate SQLite transaction without rewriting the Fresh revision.
func (a *freshApplication) reconcileCompletedAdoption(ctx context.Context) adoptCompletion {
	if a == nil || a.store == nil || a.keyApply == nil {
		return adoptCompletion{status: http.StatusConflict, code: "adopt_installation_conflict"}
	}
	a.adoptMigrationMu.Lock()
	var didBackfill bool
	var completedOp bootstrap.FreshOperation
	decision := func() adoptCompletion {
		current, err := a.store.Load(ctx)
		if err != nil {
			return adoptCompletion{status: http.StatusServiceUnavailable, code: "adopt_installation_unavailable"}
		}
		op := current.Operation
		if !op.ProductComplete || op.Strategy.Strategy != bootstrap.AdoptExisting {
			return adoptCompletion{next: current}
		}
		authority := a.keyApply.ForOperation(op)
		if authority == nil || authority.Profile() != freshkeyapply.ProfileManaged ||
			op.Strategy.Stage != "committed" || current.Connection == nil ||
			!freshkeyapply.ValidManagerOwnedDestinationCheckpoint(op.Strategy) {
			return adoptCompletion{status: http.StatusConflict, code: "adopt_installation_conflict"}
		}
		ready, err := a.keyApply.AdoptionRegistrationReady(ctx)
		if err != nil {
			if errors.Is(err, freshkeyapply.ErrAdoptionConflict) || errors.Is(err, freshkeyapply.ErrGeneration) {
				return adoptCompletion{status: http.StatusConflict, code: "adopt_installation_conflict"}
			}
			return adoptCompletion{status: http.StatusServiceUnavailable, code: "adopt_installation_unavailable"}
		}
		if ready {
			return adoptCompletion{next: current}
		}
		if a.protector == nil {
			return adoptCompletion{status: http.StatusServiceUnavailable, code: "adopt_installation_unavailable"}
		}
		managementKey, err := a.protector.UnprotectString(current.Connection.ManagementKey)
		if err != nil {
			return adoptCompletion{status: http.StatusServiceUnavailable, code: "adopt_installation_unavailable"}
		}
		if err := a.keyApply.VerifyRuntimeTarget(ctx); err != nil {
			if errors.Is(err, freshkeyapply.ErrGeneration) {
				return adoptCompletion{status: http.StatusConflict, code: "adopt_installation_conflict"}
			}
			return adoptCompletion{status: http.StatusServiceUnavailable, code: "adopt_destination_unproven"}
		}
		destinationKeys, err := a.keyApply.AdoptedDestinationKeys(ctx, managementKey)
		if err != nil {
			return adoptCompletion{status: http.StatusServiceUnavailable, code: "adopt_destination_unproven"}
		}
		prepared, err := a.keyApply.PrepareAdoption(ctx, destinationKeys)
		if err != nil {
			if errors.Is(err, freshkeyapply.ErrAdoptionConflict) || errors.Is(err, freshkeyapply.ErrGeneration) {
				return adoptCompletion{status: http.StatusConflict, code: "adopt_installation_conflict"}
			}
			return adoptCompletion{status: http.StatusServiceUnavailable, code: "adopt_installation_unavailable"}
		}
		updater, ok := a.store.(freshstore.TransactionalUpdater)
		if !ok {
			return adoptCompletion{status: http.StatusServiceUnavailable, code: "adopt_installation_unavailable"}
		}
		err = updater.WithRecordTx(ctx, func(record freshstore.Record, tx *sql.Tx) error {
			if record.Operation.Identity != op.Identity || !record.Operation.ProductComplete ||
				record.Operation.Strategy.Stage != "committed" || record.Operation.Strategy.Strategy != bootstrap.AdoptExisting {
				return freshstore.ErrConflict
			}
			if freshkeyapply.ManagedRunID(string(record.Operation.Identity.OperationID)) != prepared.RunID() ||
				strconv.FormatUint(record.Operation.Strategy.TargetGeneration, 10) != prepared.Generation() {
				return freshstore.ErrConflict
			}
			_, commitErr := a.keyApply.CommitAdoptionTx(ctx, tx, prepared, record.Operation.Strategy.TargetIdentity)
			return commitErr
		})
		if err != nil {
			switch {
			case errors.Is(err, freshkeyapply.ErrAdoptionConflict), errors.Is(err, freshkeyapply.ErrGeneration):
				return adoptCompletion{status: http.StatusConflict, code: "adopt_installation_conflict"}
			case errors.Is(err, freshstore.ErrConflict), errors.Is(err, freshstore.ErrNotFresh):
				return adoptCompletion{status: http.StatusConflict}
			default:
				return adoptCompletion{status: http.StatusServiceUnavailable, code: "adopt_installation_unavailable"}
			}
		}
		didBackfill = true
		completedOp = current.Operation
		return adoptCompletion{next: current}
	}()
	a.adoptMigrationMu.Unlock()
	if didBackfill {
		a.pluginCapability.reprove(ctx)
		a.startUsageCollection(completedOp)
	}
	return decision
}

func (a *freshApplication) awaitCompletedAdoptionBackfill(ctx context.Context) {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	decision := a.reconcileCompletedAdoption(probeCtx)
	cancel()
	if decision.status == 0 || decision.status == http.StatusConflict {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		decision = a.reconcileCompletedAdoption(probeCtx)
		cancel()
		if decision.status == 0 || decision.status == http.StatusConflict {
			return
		}
	}
}

// writeFreshAdoptCompleteError refuses one adoption completion with the code the
// operator acts on, and the retryable count when that is the reason.
func writeFreshAdoptCompleteError(w http.ResponseWriter, status int, code string, retryable int) {
	body := map[string]any{"code": code}
	if retryable > 0 {
		body["retryable"] = retryable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeFreshJSON(w, body)
}

// adoptionMigrationSession re-reads the installation record and re-validates the
// session token that authorized this adoption. A run that outlives its request
// carries no authority of its own, so every checkpoint asks the same question
// the request path asked: does this session still hold the installation?
//
// An empty token is the Admin path, which has no Fresh session to re-check; that
// request was authorized by the Admin credential on every call.
func (a *freshApplication) adoptionMigrationSession(ctx context.Context, token string) adoptMigrationSession {
	return func(record freshstore.Record) error {
		current, err := a.store.Load(ctx)
		if err != nil || current.Operation.Identity != record.Operation.Identity {
			return freshstore.ErrConflict
		}
		if token == "" {
			return nil
		}
		return a.validateInstallationSession(current, token)
	}
}

// adoptSummaryWriteAttempts bounds how many times one checkpoint re-reads the
// record after a revision conflict. A conflict is another writer having moved
// the revision between the read and the write, which the next attempt resolves;
// three is enough for the writers a committed installation has, and a conflict
// that survives them is a different installation or a stuck writer, which is a
// stop rather than a spin.
const adoptSummaryWriteAttempts = 3

// persistAdoptionSummary returns the executor's per-item checkpoint. Each call
// appends exactly the item the run just resolved, through the same revision CAS
// as every other Fresh write, so a recorded success can never describe a write
// that did not happen and can never be rewritten.
//
// The checkpoint is bound to one run generation. Every attempt re-reads the
// record and refuses when the durable summary belongs to a different run, and the
// CAS callback repeats that comparison on the record it is about to write: a
// checkpoint retry that outlived its run — a slow item, a blocked write, a
// process that resumed meanwhile — can never overwrite the newer run's item list,
// even though both runs may look identical to a reader.
//
// A revision conflict is not a failure of the item: the copy resolved nothing
// extra, and another writer only moved the record forward. The checkpoint
// therefore re-reads the record and retries the append under the new revision,
// at most adoptSummaryWriteAttempts times. It stops when the conflict persists,
// and it stops immediately when the record it re-read is no longer this adoption
// still at committed, or no longer this run.
func persistAdoptionSummary(ctx context.Context, a *freshApplication, identity bootstrap.FreshIdentity, runID string, session adoptMigrationSession) freshadopt.Checkpoint {
	return func(summary freshstore.AdoptionSummary) error {
		if runID == "" || summary.RunID != runID {
			// The executor's clone is the run's own plan; a summary that carries a
			// different generation is not this checkpoint's to write.
			return freshstore.ErrConflict
		}
		for range adoptSummaryWriteAttempts {
			current, err := a.store.Load(ctx)
			if err != nil {
				return err
			}
			if current.Operation.Identity != identity || !adoptionMigrationCommitted(current) ||
				current.AdoptionSummary == nil || current.AdoptionSummary.RunID != runID {
				return freshstore.ErrConflict
			}
			// The session is re-checked before the write, not once at the start: a
			// migration runs for minutes after the reply that started it.
			if session != nil {
				if err := session(current); err != nil {
					return err
				}
			}
			// The revision is the one just read: a conflict means another writer
			// moved the record between this read and the write, which the next
			// attempt resolves from its own read. The CAS still checks the whole
			// record, and the callback re-checks the run generation on the record
			// it is about to write.
			_, err = a.store.Update(ctx, identity, current.Operation.Revision, func(record *freshstore.Record) error {
				if !adoptionMigrationCommitted(*record) ||
					record.AdoptionSummary == nil || record.AdoptionSummary.RunID != runID {
					return freshstore.ErrConflict
				}
				copied := summary
				record.AdoptionSummary = &copied
				return nil
			})
			if err == nil {
				return nil
			}
			if !errors.Is(err, freshstore.ErrConflict) {
				return err
			}
			// The next attempt re-reads and decides: only the same adoption still at
			// committed, still carrying this run's generation, is a record this run
			// may write to again.
		}
		return freshstore.ErrConflict
	}
}

// adoptionMigrationSettled reports whether the durable summary has no further
// work for a run to do: the last run reached the end of the plan it fixed and
// left no retryable item behind.
//
// It applies the record's own completion rules — the `Finished`/`Planned` pair
// and the item-level `Retryable` classification — rather than a parallel
// predicate, so "settled" here means exactly what the completion accepts as a
// finished migration. An unfinished run, or one with an item whose source read
// wrote nothing, still needs a run and gets a new generation.
func adoptionMigrationSettled(summary freshstore.AdoptionSummary) bool {
	if !summary.Finished || len(summary.Items) < summary.Planned {
		return false
	}
	for _, item := range summary.Items {
		if summary.Retryable(item.Item) {
			return false
		}
	}
	return true
}

// adoptionMigrationCommitted reports whether one record is still the committed,
// unfinished adoption this migration writes into. A complete installation is
// never a destination for a run: the copy would overwrite what the operator chose
// after completion, and no later replay may reopen it.
func adoptionMigrationCommitted(record freshstore.Record) bool {
	return record.Operation.Strategy.Strategy == bootstrap.AdoptExisting &&
		record.Operation.Strategy.SourceClass == adoptMigrationSourceClass &&
		record.Operation.Strategy.Stage == "committed" &&
		!record.Operation.ProductComplete
}

// adoptionMigrationCode bounds a migration error to the code the log may carry.
// The executor's errors are this package's own typed values; anything else is
// reported as unavailable rather than interpolated.
func adoptionMigrationCode(err error) error {
	switch {
	case errors.Is(err, freshadopt.ErrInventoryUnavailable):
		return errors.New("source_inventory_unavailable")
	case errors.Is(err, freshadopt.ErrEndpointInvalid):
		return errors.New("endpoint_invalid")
	default:
		return errors.New("migration_unavailable")
	}
}
