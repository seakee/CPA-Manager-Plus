package main

import (
	"context"
	"errors"
	"log"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshadopt"
)

// adoptMigrationSourceClass is the only source class the v8 migration executor
// can read. A legacy_managed source is refused before this point, but the check
// is repeated here so the executor is never pointed at a process whose
// management surface is not v8.
const adoptMigrationSourceClass = bootstrap.AdoptNetworkExisting

// runAdoptionMigration copies the bounded inventory from the source CPA to the
// destination this operation has just committed, checkpointing one result per
// item into the Fresh record.
//
// This runs only after the destination is committed and its client keys are
// verified, so every write lands on an installation that already owns its
// identity. It is called again on a committed replay, which is what makes the
// migration resumable: the executor skips every item the record already
// settled, and an item whose write result was unknown is reconciled by reading
// the destination back rather than by sending the write again.
//
// Nothing here can fail the adoption. The destination is already committed and
// verified; a manual item, a failed item and an unreachable source are all
// reported in the summary for the completion screen (Step 4 reads it), and a
// summary that could not be written leaves the run resumable from the items it
// did record.
func (a *freshApplication) runAdoptionMigration(ctx context.Context, record freshstore.Record, sourceBaseURL, sourceManagementKey string) {
	if record.Connection == nil || a.protector == nil || record.Operation.Strategy.Strategy != bootstrap.AdoptExisting ||
		record.Operation.Strategy.SourceClass != adoptMigrationSourceClass || record.Operation.Strategy.Stage != "committed" {
		return
	}
	destinationManagementKey, err := a.protector.UnprotectString(record.Connection.ManagementKey)
	if err != nil {
		// The committed destination credential is unreadable, so no destination
		// write is possible. The adoption itself stays committed and complete.
		log.Printf("Fresh adoption migration skipped: destination credential unavailable")
		return
	}
	source, err := freshadopt.NewSource(freshadopt.Endpoint{BaseURL: sourceBaseURL, ManagementKey: sourceManagementKey})
	if err != nil {
		log.Printf("Fresh adoption migration skipped: source endpoint invalid")
		return
	}
	destination, err := freshadopt.NewDestination(freshadopt.Endpoint{BaseURL: record.Connection.CPABaseURL, ManagementKey: destinationManagementKey})
	if err != nil {
		log.Printf("Fresh adoption migration skipped: destination endpoint invalid")
		return
	}
	previous := freshstore.AdoptionSummary{}
	if record.AdoptionSummary != nil {
		previous = *record.AdoptionSummary
	}
	result, err := freshadopt.New(source, destination).Run(ctx, previous, persistAdoptionSummary(ctx, a, record.Operation.Identity, record.Operation.Revision))
	if err != nil {
		// The reason is a bounded code from this package, never an upstream
		// message: a source refusal can quote a value.
		log.Printf("Fresh adoption migration stopped after %d items: %v", len(result.Summary.Items), adoptionMigrationCode(err))
		return
	}
	migrated, manual, failed := result.Summary.Counts()
	log.Printf("Fresh adoption migration recorded %d migrated, %d manual, %d failed items", migrated, manual, failed)
}

// persistAdoptionSummary returns the executor's per-item checkpoint. Each call
// appends exactly the item the run just resolved, through the same revision CAS
// as every other Fresh write, so a recorded success can never describe a write
// that did not happen and can never be rewritten.
func persistAdoptionSummary(ctx context.Context, a *freshApplication, identity bootstrap.FreshIdentity, revision uint64) freshadopt.Checkpoint {
	return func(summary freshstore.AdoptionSummary) error {
		next, err := a.store.Update(ctx, identity, revision, func(record *freshstore.Record) error {
			if record.Operation.Strategy.Strategy != bootstrap.AdoptExisting || record.Operation.Strategy.SourceClass != adoptMigrationSourceClass {
				return freshstore.ErrConflict
			}
			copied := summary
			record.AdoptionSummary = &copied
			return nil
		})
		if err != nil {
			return err
		}
		revision = next.Operation.Revision
		return nil
	}
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
