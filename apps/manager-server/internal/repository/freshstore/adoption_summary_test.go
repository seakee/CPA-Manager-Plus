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
