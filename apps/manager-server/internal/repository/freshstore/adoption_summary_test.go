package freshstore

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

// adoptionSummaryStore returns a committed adoption record plus the helpers the
// summary tests use: append one summary through the ordinary CAS and read it
// back.
func adoptionSummaryStore(t *testing.T) (Store, bootstrap.FreshIdentity, Record) {
	t.Helper()
	store, id, record := newAdoptionAttemptStore(t, true)
	return store, id, record
}

// writeSummary applies one summary to the record under the current revision and
// returns the advanced record.
func writeSummary(t *testing.T, store Store, id bootstrap.FreshIdentity, current Record, summary AdoptionSummary) (Record, error) {
	t.Helper()
	return store.Update(t.Context(), id, current.Operation.Revision, func(r *Record) error {
		copied := summary
		r.AdoptionSummary = &copied
		return nil
	})
}

func summaryOf(items ...AdoptionItem) AdoptionSummary { return AdoptionSummary{Items: items} }

func migratedItem(name string) AdoptionItem {
	return AdoptionItem{Item: name, Kind: AdoptionKindConfigLeaf, Outcome: AdoptionMigrated}
}

// The summary is append-only: a recorded item may not be rewritten or erased,
// because every outcome is the evidence a replay and the completion screen read.
func TestAdoptionSummaryIsAppendOnly(t *testing.T) {
	store, id, record := adoptionSummaryStore(t)
	first := summaryOf(migratedItem("config:routing.strategy"))
	record, err := writeSummary(t, store, id, record, first)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*AdoptionSummary){
		"rewrite": func(s *AdoptionSummary) { s.Items[0].Outcome = AdoptionManual },
		"erase":   func(s *AdoptionSummary) { s.Items = nil },
		"rename":  func(s *AdoptionSummary) { s.Items[0].Item = "config:routing.cooldown" },
	} {
		next := first
		mutate(&next)
		if _, err := writeSummary(t, store, id, record, next); !errors.Is(err, ErrConflict) {
			t.Fatalf("%s was accepted: %v", name, err)
		}
	}
	// Appending is what the executor does, and it stays accepted.
	appended := summaryOf(migratedItem("config:routing.strategy"), migratedItem("config:routing.cooldown"))
	updated, err := writeSummary(t, store, id, record, appended)
	if err != nil {
		t.Fatalf("append refused: %v", err)
	}
	if loaded, err := store.Load(t.Context()); err != nil || loaded.AdoptionSummary == nil || len(loaded.AdoptionSummary.Items) != 2 {
		t.Fatalf("the appended summary did not survive the reload: %+v %v", loaded.AdoptionSummary, err)
	}
	if updated.Operation.Revision <= record.Operation.Revision {
		t.Fatal("a summary write did not advance the revision")
	}
}

// The one permitted in-place change is the provisional record of a failure that
// wrote nothing: a later run replaces it where it stands, and nothing else may
// move.
func TestAdoptionSummaryReplacesProvisionalFailuresInPlace(t *testing.T) {
	store, id, record := adoptionSummaryStore(t)
	provisional := summaryOf(
		migratedItem("config:routing.strategy"),
		AdoptionItem{Item: "credential:claude-a.json", Kind: AdoptionKindCredential, Outcome: AdoptionFailed, Reason: "download_failed"},
	)
	record, err := writeSummary(t, store, id, record, provisional)
	if err != nil {
		t.Fatal(err)
	}
	// A settled failure is never replaced: its only resolution is the read-back
	// that produced it. It is appended next to the provisional one first.
	settledItem := AdoptionItem{Item: "credential:codex-b.json", Kind: AdoptionKindCredential, Outcome: AdoptionFailed, Reason: "apply_unknown"}
	settledRecord, err := writeSummary(t, store, id, record, summaryOf(provisional.Items[0], provisional.Items[1], settledItem))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeSummary(t, store, id, settledRecord, summaryOf(
		provisional.Items[0], provisional.Items[1],
		AdoptionItem{Item: "credential:codex-b.json", Kind: AdoptionKindCredential, Outcome: AdoptionMigrated},
	)); !errors.Is(err, ErrConflict) {
		t.Fatal("a settled failure was rewritten", err)
	}
	record = settledRecord
	// The retryable one is replaced by its real outcome, in the same position.
	resolved := summaryOf(
		provisional.Items[0],
		AdoptionItem{Item: "credential:claude-a.json", Kind: AdoptionKindCredential, Outcome: AdoptionMigrated},
		settledItem,
	)
	if _, err := writeSummary(t, store, id, record, resolved); err != nil {
		t.Fatalf("a retryable failure could not be resolved: %v", err)
	}
	// It may not become a different item, though.
	moved := summaryOf(
		provisional.Items[0],
		AdoptionItem{Item: "credential:gemini-c.json", Kind: AdoptionKindCredential, Outcome: AdoptionMigrated},
		settledItem,
	)
	if _, err := writeSummary(t, store, id, record, moved); !errors.Is(err, ErrConflict) {
		t.Fatal("a provisional record was replaced by a different item", err)
	}
}

// The store's own bounds are the only ones a producer may plan against, and a
// record past them is refused rather than truncated.
func TestAdoptionSummaryEnforcesItsSizeBounds(t *testing.T) {
	if err := validAdoptionSummary(summaryOf()); err != nil {
		t.Fatalf("an empty summary must be valid: %v", err)
	}
	items := make([]AdoptionItem, 0, maxAdoptionItems+1)
	for index := 0; index <= maxAdoptionItems; index++ {
		items = append(items, migratedItem(fmt.Sprintf("config:leaf-%06d", index)))
	}
	if err := validAdoptionSummary(summaryOf(items...)); err == nil {
		t.Fatal("a summary past the item bound was accepted")
	}
	longName := "credential:" + strings.Repeat("a", maxAdoptionItemName)
	if err := validAdoptionSummary(summaryOf(migratedItem(longName))); err == nil {
		t.Fatal("an item name past the bound was accepted")
	}
	longReason := migratedItem("config:routing.strategy")
	longReason.Outcome, longReason.Reason = AdoptionManual, strings.Repeat("r", maxAdoptionReason+1)
	if err := validAdoptionSummary(summaryOf(longReason)); err == nil {
		t.Fatal("a reason past the bound was accepted")
	}
	// The published bounds are the ones the checker applies.
	if MaxAdoptionItems() != maxAdoptionItems || MaxAdoptionItemName() != maxAdoptionItemName {
		t.Fatal("the published bounds differ from the enforced ones")
	}
}

// A run that stops early records a plan it never reached, and completion reads
// exactly that: `Finished` plus the item count against `Planned`. The marker may
// only move forward once the installation is complete, because reopening a
// finished migration would claim a finished installation still has items to copy.
func TestAdoptionSummaryFinishedIsMonotoneAfterCompletion(t *testing.T) {
	store, id, record := adoptionSummaryStore(t)
	item := migratedItem("config:routing.strategy")
	finished := AdoptionSummary{Items: []AdoptionItem{item}, Finished: true, Planned: 1}
	record, err := writeSummary(t, store, id, record, finished)
	if err != nil {
		t.Fatal(err)
	}
	reopened := finished
	reopened.Finished = false
	// Before completion a later run may reopen the marker: it writes its own
	// unfinished plan, which is what a replay that stopped again looks like.
	if record, err = writeSummary(t, store, id, record, reopened); err != nil {
		t.Fatalf("an unfinished replay was refused before completion: %v", err)
	}
	record, err = writeSummary(t, store, id, record, finished)
	if err != nil {
		t.Fatal(err)
	}
	record, err = store.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error {
		// Completion needs a committed routing checkpoint and an initialized
		// admin, which this fixture has not written yet.
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
			OperationID: r.Operation.Identity.OperationID, Revision: 1,
			Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
		}
		r.Operation.ProductComplete = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeSummary(t, store, id, record, reopened); !errors.Is(err, ErrConflict) {
		t.Fatalf("a completed installation reopened its migration: %v", err)
	}
}

// `Planned` is the plan, so the store bounds it and refuses a finished run whose
// item list never reached it. Both halves are what make completion's
// `len(Items) >= Planned` guard mean something.
func TestAdoptionSummaryPlannedIsBounded(t *testing.T) {
	item := migratedItem("config:routing.strategy")
	for name, summary := range map[string]AdoptionSummary{
		"negative":       {Items: []AdoptionItem{item}, Planned: -1},
		"past the bound": {Items: []AdoptionItem{item}, Planned: maxAdoptionItems + 1},
		"finished short": {Items: []AdoptionItem{item}, Finished: true, Planned: 2},
	} {
		if err := validAdoptionSummary(summary); err == nil {
			t.Fatalf("%s plan accepted", name)
		}
	}
	// An unfinished run whose plan is larger than its prefix is the ordinary
	// state of a stop, and a finished run that reached its plan is the ordinary
	// state of an end.
	if err := validAdoptionSummary(AdoptionSummary{Items: []AdoptionItem{item}, Planned: 2}); err != nil {
		t.Fatalf("an unfinished plan was refused: %v", err)
	}
	if err := validAdoptionSummary(AdoptionSummary{Items: []AdoptionItem{item}, Finished: true, Planned: 1}); err != nil {
		t.Fatalf("a finished plan was refused: %v", err)
	}
	store, id, record := adoptionSummaryStore(t)
	if _, err := writeSummary(t, store, id, record, AdoptionSummary{Items: []AdoptionItem{item}, Finished: true, Planned: 2}); err == nil {
		t.Fatal("the store recorded a finished run short of its plan")
	}
	loaded, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AdoptionSummary != nil {
		t.Fatalf("a refused summary was recorded anyway: %+v", loaded.AdoptionSummary)
	}
}

// The clone is what a CAS writes back, so it may never share an item with the
// record it was read from: a later in-place edit would rewrite a recorded result
// without going through the store.
func TestAdoptionSummaryCloneIsIndependent(t *testing.T) {
	original := &AdoptionSummary{Items: []AdoptionItem{migratedItem("config:routing.strategy")}, Finished: true, Planned: 1}
	clone := cloneAdoptionSummary(original)
	if clone == original || clone.Planned != 1 || !clone.Finished || len(clone.Items) != 1 {
		t.Fatalf("the clone does not describe the same summary: %+v", clone)
	}
	if &clone.Items[0] == &original.Items[0] {
		t.Fatal("the clone shares its item array with the record")
	}
	clone.Items[0] = migratedItem("config:routing.cooldown")
	clone.Planned, clone.Finished = 4, false
	if original.Items[0].Item != "config:routing.strategy" || original.Planned != 1 || !original.Finished {
		t.Fatalf("a clone write reached the record: %+v", *original)
	}
	if cloneAdoptionSummary(nil) != nil {
		t.Fatal("a nil summary cloned to a value")
	}
}

// The absent-at-source outcome is part of the recorded vocabulary: it is what
// keeps a replay from overwriting a value the operator set after completion.
func TestAdoptionSummaryAcceptsAbsentAtSource(t *testing.T) {
	absent := AdoptionItem{Item: "config:routing.cooldown", Kind: AdoptionKindConfigLeaf, Outcome: AdoptionAbsentAtSource}
	if err := validAdoptionSummary(summaryOf(absent)); err != nil {
		t.Fatalf("an absent_at_source item must be valid: %v", err)
	}
	settled := summaryOf(absent)
	if !settled.Settled(absent.Item) {
		t.Fatal("an absent_at_source item must be settled")
	}
	migrated, _, failed := settled.Counts()
	if migrated != 1 || failed != 0 {
		t.Fatalf("an absent_at_source item counted as migrated=%d failed=%d", migrated, failed)
	}
	withInstruction := absent
	withInstruction.Instruction = "copy it"
	if err := validAdoptionSummary(summaryOf(withInstruction)); err == nil {
		t.Fatal("an absent_at_source item with an instruction was accepted")
	}
}

// A summary belongs to an adoption and to nothing else: a record of any other
// strategy must never be able to carry one.
func TestAdoptionSummaryIsRefusedForAnotherStrategy(t *testing.T) {
	store, id, record := adoptionSummaryStore(t)
	record, err := writeSummary(t, store, id, record, summaryOf(migratedItem("config:routing.strategy")))
	if err != nil {
		t.Fatal(err)
	}
	// Moving the record to another strategy cannot succeed either: the strategy
	// guard runs before the summary is read or written.
	if _, err := store.Update(t.Context(), id, record.Operation.Revision, func(r *Record) error {
		r.Operation.Strategy.Strategy = bootstrap.NewManaged
		return nil
	}); err == nil {
		t.Fatal("a summary-carrying record became a non-adoption")
	}
	loaded, err := store.Load(t.Context())
	if err != nil || loaded.Operation.Strategy.Strategy != bootstrap.AdoptExisting {
		t.Fatalf("the strategy changed anyway: %+v %v", loaded.Operation.Strategy, err)
	}
}

// A committed adoption record keeps its summary across a restart, and the
// connection it names is unchanged by the summary writes.
func TestAdoptionSummarySurvivesReloadWithItsConnection(t *testing.T) {
	store, id, record := adoptionSummaryStore(t)
	record, err := writeSummary(t, store, id, record, summaryOf(migratedItem("config:routing.strategy")))
	if err != nil {
		t.Fatal(err)
	}
	connected := record.Connection
	if connected == nil {
		t.Fatal("the fixture committed no destination")
	}
	loaded, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AdoptionSummary == nil || len(loaded.AdoptionSummary.Items) != 1 {
		t.Fatalf("the summary did not reload: %+v", loaded.AdoptionSummary)
	}
	if loaded.Connection == nil || *loaded.Connection != *connected {
		t.Fatalf("the summary write changed the destination connection: %+v", loaded.Connection)
	}
}

// The run identity is part of the record's bounded shape: absent on a record
// written before generations existed, otherwise exactly one 128-bit hex value.
// It may only be taken over by a new, unfinished run, which is what makes an
// acknowledgement of one run's retryable items inapplicable to another.
func TestAdoptionSummaryRunGenerationIsBoundedAndReplacedOnlyAtARunStart(t *testing.T) {
	for name, runID := range map[string]string{
		"short":   strings.Repeat("a", 31),
		"long":    strings.Repeat("a", 33),
		"upper":   strings.Repeat("A", 32),
		"non-hex": strings.Repeat("g", 32),
	} {
		if validAdoptionRunID(runID) {
			t.Fatalf("a %s run identity was accepted", name)
		}
		invalid := summaryOf(migratedItem("config:routing.strategy"))
		invalid.RunID = runID
		if err := validAdoptionSummary(invalid); err == nil {
			t.Fatalf("a %s run identity passed the summary validator", name)
		}
	}
	if !validAdoptionRunID("") || !validAdoptionRunID(strings.Repeat("a", 32)) {
		t.Fatal("a valid run identity was refused")
	}

	store, id, record := adoptionSummaryStore(t)
	item := migratedItem("config:routing.strategy")
	// The legacy shape loads and is never invented into a generation.
	record, err := writeSummary(t, store, id, record, summaryOf(item))
	if err != nil {
		t.Fatalf("a legacy summary was refused: %v", err)
	}
	legacy, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if legacy.AdoptionSummary == nil || legacy.AdoptionSummary.RunID != "" || len(legacy.AdoptionSummary.Items) != 1 {
		t.Fatalf("the legacy summary did not reload: %+v", legacy.AdoptionSummary)
	}

	runA := strings.Repeat("a", 32)
	withA := summaryOf(item)
	withA.RunID = runA
	record, err = writeSummary(t, store, id, record, withA)
	if err != nil {
		t.Fatalf("a legacy record could not start a run: %v", err)
	}
	if clone := cloneAdoptionSummary(record.AdoptionSummary); clone == nil || clone.RunID != runA {
		t.Fatalf("the clone dropped the run identity: %+v", clone)
	}

	// A new run takes the record over while it is unfinished.
	runB := strings.Repeat("b", 32)
	withB := withA
	withB.RunID, withB.Finished = runB, false
	record, err = writeSummary(t, store, id, record, withB)
	if err != nil {
		t.Fatalf("a new run could not take the record over: %v", err)
	}
	// The run finishes under its own generation.
	finished := withB
	finished.Finished = true
	record, err = writeSummary(t, store, id, record, finished)
	if err != nil {
		t.Fatalf("a run could not finish under its own generation: %v", err)
	}
	// A finished run may not change its generation, and a generation may not be
	// cleared back to the legacy absent value.
	replaced := finished
	replaced.RunID = strings.Repeat("c", 32)
	if _, err := writeSummary(t, store, id, record, replaced); !errors.Is(err, ErrConflict) {
		t.Fatalf("a finished run's generation was replaced: %v", err)
	}
	cleared := finished
	cleared.RunID = ""
	if _, err := writeSummary(t, store, id, record, cleared); !errors.Is(err, ErrConflict) {
		t.Fatalf("a persisted generation was cleared: %v", err)
	}
	loaded, err := store.Load(t.Context())
	if err != nil || loaded.AdoptionSummary == nil || loaded.AdoptionSummary.RunID != runB {
		t.Fatalf("the refused writes changed the record: %+v %v", loaded.AdoptionSummary, err)
	}
}
