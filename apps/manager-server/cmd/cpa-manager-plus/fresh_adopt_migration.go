package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
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
		revision := current.Operation.Revision
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
		checkpoint := persistAdoptionSummary(ctx, a, identity, revision, a.adoptionMigrationSession(ctx, sessionToken))
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
func (a *freshApplication) startAdoptionMigration(ctx context.Context, identity bootstrap.FreshIdentity, run func(context.Context, freshstore.Record)) string {
	a.adoptMigrationMu.Lock()
	if a.adoptMigrationRunning {
		a.adoptMigrationMu.Unlock()
		return adoptMigrationStateRunning
	}
	current, err := a.store.Load(ctx)
	if err != nil || current.Operation.Identity != identity || !adoptionMigrationCommitted(current) {
		// Not this adoption at committed any more — complete, re-staged or
		// replaced: a run would write into an installation this replay no longer
		// describes, so none is started.
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

// adoptMigrationInFlight reports whether a background copy is running right now.
// It is the read half of the single-flight lock, so the completion path and the
// start path agree on what "running" means. A caller that also decides something
// under the lock takes `adoptMigrationMu` itself instead.
func (a *freshApplication) adoptMigrationInFlight() bool {
	a.adoptMigrationMu.Lock()
	defer a.adoptMigrationMu.Unlock()
	return a.adoptMigrationRunning
}

// adoptCompleteRequest is the only field a completion request may carry. An
// adoption's conditions are the record's, so the body never states them.
type adoptCompleteRequest struct {
	// AcceptRetryable lets the operator finish over items a run could not read
	// from the source and wrote nothing for. It never accepts an item whose write
	// result is unknown: that one's only resolution is its own read-back.
	AcceptRetryable bool `json:"acceptRetryable,omitempty"`
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
//  6. no item is retryable, unless the request accepts them.
//
// Everything is decided while `adoptMigrationMu` is held, and the CAS that sets
// `ProductComplete` runs under the same lock, so a run can never start between
// the last check and the write.
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
		// A replay is also the retry trigger for a completion whose usage startup
		// failed. It changes nothing and re-asks the idempotent startup path.
		a.startUsageCollection(op)
		writeFreshJSON(w, op)
		return
	}
	if op.Strategy.Stage != "committed" || record.Connection == nil || a.protector == nil || a.keyApply == nil ||
		!a.keyApply.Targets(op, freshkeyapply.SurfaceKeyOperation) {
		w.WriteHeader(http.StatusConflict)
		return
	}
	managementKey, err := a.protector.UnprotectString(record.Connection.ManagementKey)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	// Everything below is decided while the migration lock is held. A run that is
	// starting holds the same lock, so it can neither slip between these checks and
	// the CAS nor be decided away from them: the completion either waits for the
	// run to settle, or the run waits until the installation is already complete.
	a.adoptMigrationMu.Lock()
	defer a.adoptMigrationMu.Unlock()
	if a.adoptMigrationRunning {
		// The operator retries once the copy has settled. Nothing is written.
		writeFreshAdoptCompleteError(w, http.StatusConflict, "adopt_migration_running", 0)
		return
	}
	// One authenticated read of the destination is conditions 3 and 4: the Adopt
	// posture proves the key this Manager installed, and the snapshot carries the
	// client keys the destination actually has.
	if _, err := a.keyApply.AdoptedDestinationKeys(r.Context(), managementKey); err != nil {
		writeFreshAdoptCompleteError(w, http.StatusServiceUnavailable, "adopt_destination_unproven", 0)
		return
	}
	summary := freshstore.AdoptionSummary{}
	if record.AdoptionSummary != nil {
		summary = *record.AdoptionSummary
	}
	if !summary.Finished || len(summary.Items) < summary.Planned {
		writeFreshAdoptCompleteError(w, http.StatusConflict, "adopt_migration_incomplete", 0)
		return
	}
	retryable := 0
	for _, item := range summary.Items {
		if summary.Retryable(item.Item) {
			retryable++
		}
	}
	if retryable > 0 && !request.AcceptRetryable {
		writeFreshAdoptCompleteError(w, http.StatusConflict, "adopt_migration_retryable", retryable)
		return
	}
	next, err := a.store.Update(r.Context(), op.Identity, op.Revision, func(record *freshstore.Record) error {
		if freshAuth {
			if err := a.authority.ValidateContinuation(*record, continuation); err != nil {
				return err
			}
		}
		if record.Operation.Strategy.Stage != "committed" || record.Operation.Strategy.Strategy != bootstrap.AdoptExisting || record.Operation.ProductComplete {
			return freshstore.ErrConflict
		}
		record.Operation.ProductComplete = true
		return nil
	})
	if err != nil {
		w.WriteHeader(http.StatusConflict)
		return
	}
	// Completion is the moment the install-derived CPA authority exists, so the
	// same idempotent startup path as a resumed install runs now.
	a.startUsageCollection(next.Operation)
	writeFreshJSON(w, next.Operation)
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
// A revision conflict is not a failure of the item: the copy resolved nothing
// extra, and another writer only moved the record forward. The checkpoint
// therefore re-reads the record and retries the append under the new revision,
// at most adoptSummaryWriteAttempts times. It stops when the conflict persists,
// and it stops immediately when the record it re-read is no longer this adoption
// still at committed — that is not a race but a record this run must not write.
func persistAdoptionSummary(ctx context.Context, a *freshApplication, identity bootstrap.FreshIdentity, revision uint64, session adoptMigrationSession) freshadopt.Checkpoint {
	return func(summary freshstore.AdoptionSummary) error {
		for range adoptSummaryWriteAttempts {
			// The session is re-checked before the write, not once at the start: a
			// migration runs for minutes after the reply that started it.
			if session != nil {
				current, err := a.store.Load(ctx)
				if err != nil {
					return err
				}
				if err := session(current); err != nil {
					return err
				}
			}
			next, err := a.store.Update(ctx, identity, revision, func(record *freshstore.Record) error {
				if !adoptionMigrationCommitted(*record) {
					return freshstore.ErrConflict
				}
				copied := summary
				record.AdoptionSummary = &copied
				return nil
			})
			if err == nil {
				revision = next.Operation.Revision
				return nil
			}
			if !errors.Is(err, freshstore.ErrConflict) {
				return err
			}
			// Re-read and decide: only the same adoption still at committed is a
			// record this run may write to again.
			current, loadErr := a.store.Load(ctx)
			if loadErr != nil {
				return loadErr
			}
			if current.Operation.Identity != identity || !adoptionMigrationCommitted(current) {
				return freshstore.ErrConflict
			}
			revision = current.Operation.Revision
		}
		return freshstore.ErrConflict
	}
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
