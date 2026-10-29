package main

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshadopt"
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
	previous := freshstore.AdoptionSummary{}
	if record.AdoptionSummary != nil {
		previous = *record.AdoptionSummary
	}
	identity, revision := record.Operation.Identity, record.Operation.Revision
	a.startAdoptionMigration(func(ctx context.Context) {
		// Both clients fix their DNS resolution once and are closed with the run.
		// A source or destination that cannot be reached ends the run here; the
		// adoption itself is already committed.
		source, err := freshadopt.NewSource(ctx, freshadopt.Endpoint{BaseURL: sourceBaseURL, ManagementKey: sourceManagementKey})
		if err != nil {
			log.Printf("Fresh adoption migration skipped: source endpoint invalid")
			return
		}
		defer source.Close()
		destination, err := freshadopt.NewDestination(ctx, freshadopt.Endpoint{BaseURL: record.Connection.CPABaseURL, ManagementKey: destinationManagementKey})
		if err != nil {
			log.Printf("Fresh adoption migration skipped: destination endpoint invalid")
			return
		}
		defer destination.Close()
		// The checkpoint is built from the run's own context, not the request's:
		// the reply has already returned, and every write this run makes has to
		// outlive it.
		checkpoint := persistAdoptionSummary(ctx, a, identity, revision, a.adoptionMigrationSession(ctx, sessionToken))
		result, runErr := freshadopt.New(source, destination, record.Operation.Strategy.AdoptionDecisions).Run(ctx, previous, checkpoint)
		if runErr != nil {
			// Only the bounded code is logged. A runner error can quote an upstream
			// value, and the code is the whole diagnosis the log needs.
			log.Printf("Fresh adoption migration stopped after %d items: %v", len(result.Summary.Items), adoptionMigrationCode(runErr))
			return
		}
		migrated, manual, failed := result.Summary.Counts()
		log.Printf("Fresh adoption migration recorded %d migrated, %d manual, %d failed items", migrated, manual, failed)
	})
	// Started here or already in flight: either way the copy is running now. The
	// reply says exactly that and nothing more; the per-item progress is the
	// summary the record carries.
	return adoptMigrationStateRunning
}

// startAdoptionMigration starts one background run and reports whether this call
// started it. It is the single-flight lock of the migration: the check and the
// start happen under one mutex, so two concurrent committed replays cannot both
// begin a copy, and the flag is cleared only when the run has finished so a
// later replay can resume a run that stopped.
//
// The run is derived from the host lifetime, not from the request: the reply
// returns at once, and the request that started the copy ends long before it
// does. The host lifetime is the right parent because the copy writes into this
// process's installation, so a host shutdown must cancel it rather than leave a
// goroutine writing into a closing database; the deadline bounds it besides.
func (a *freshApplication) startAdoptionMigration(run func(context.Context)) bool {
	a.adoptMigrationMu.Lock()
	defer a.adoptMigrationMu.Unlock()
	if a.adoptMigrationRunning {
		return false
	}
	basis := a.runCtx
	if basis == nil {
		basis = context.Background()
	}
	a.adoptMigrationRunning = true
	ctx, cancel := context.WithTimeout(basis, adoptMigrationDeadline)
	go func() {
		defer cancel()
		defer func() {
			a.adoptMigrationMu.Lock()
			a.adoptMigrationRunning = false
			a.adoptMigrationMu.Unlock()
		}()
		run(ctx)
	}()
	return true
}

// adoptMigrationInFlight reports whether a background copy is running right now.
// It is the read half of the single-flight lock, so the completion path and the
// start path agree on what "running" means.
func (a *freshApplication) adoptMigrationInFlight() bool {
	a.adoptMigrationMu.Lock()
	defer a.adoptMigrationMu.Unlock()
	return a.adoptMigrationRunning
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

// adoptionMigrationCommitted reports whether one record is still the committed
// adoption this migration writes into.
func adoptionMigrationCommitted(record freshstore.Record) bool {
	return record.Operation.Strategy.Strategy == bootstrap.AdoptExisting &&
		record.Operation.Strategy.SourceClass == adoptMigrationSourceClass &&
		record.Operation.Strategy.Stage == "committed"
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
