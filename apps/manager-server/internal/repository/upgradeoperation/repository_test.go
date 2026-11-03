package upgradeoperation_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
)

func newRepository(t *testing.T) (*upgradeoperation.Repository, *sql.DB) {
	t.Helper()
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "manager.sqlite"))
	if err != nil {
		t.Fatalf("open manager database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return upgradeoperation.New(db), db
}

func TestGetBeforeFirstAdvanceReportsNoRow(t *testing.T) {
	repository, _ := newRepository(t)
	_, found, err := repository.Get(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if found {
		t.Fatal("empty upgrade_operations table reported a row")
	}
}

func TestAdvanceRecordsStageAndDomains(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	if _, err := repository.Advance(ctx, model.UpgradeDomainUsageEvents, model.UpgradeStageCopyIn); err != nil {
		t.Fatalf("advance copy_in: %v", err)
	}
	advanced, err := repository.Advance(ctx, model.UpgradeDomainPricing, model.UpgradeStageReconcile)
	if err != nil {
		t.Fatalf("advance reconcile: %v", err)
	}
	if advanced.Stage != model.UpgradeStageReconcile {
		t.Fatalf("stage = %q, want %q", advanced.Stage, model.UpgradeStageReconcile)
	}
	stored, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get after advance: found=%t err=%v", found, err)
	}
	if stored.Stage != model.UpgradeStageReconcile {
		t.Fatalf("stored stage = %q, want %q", stored.Stage, model.UpgradeStageReconcile)
	}
	want := []string{model.UpgradeDomainUsageEvents, model.UpgradeDomainPricing}
	if len(stored.DomainsDone) != len(want) {
		t.Fatalf("domains = %v, want %v", stored.DomainsDone, want)
	}
	for index, domain := range want {
		if stored.DomainsDone[index] != domain {
			t.Fatalf("domains = %v, want %v", stored.DomainsDone, want)
		}
	}
	if stored.CreatedAtMS <= 0 || stored.UpdatedAtMS < stored.CreatedAtMS {
		t.Fatalf("timestamps = %d/%d, want created > 0 and updated >= created", stored.CreatedAtMS, stored.UpdatedAtMS)
	}
}

func TestAdvanceRejectsStageRegressionWithoutChangingTheRow(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	if _, err := repository.Advance(ctx, model.UpgradeDomainUsageEvents, model.UpgradeStageCopyIn); err != nil {
		t.Fatalf("advance copy_in: %v", err)
	}
	if _, err := repository.Advance(ctx, model.UpgradeDomainPricing, model.UpgradeStageVerify); err != nil {
		t.Fatalf("advance verify: %v", err)
	}
	before, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get before regression: %v", err)
	}
	if _, err := repository.Advance(ctx, model.UpgradeDomainCoverage, model.UpgradeStageCopyIn); !errors.Is(err, upgradeoperation.ErrStageRegression) {
		t.Fatalf("regressing advance error = %v, want ErrStageRegression", err)
	}
	after, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get after regression: %v", err)
	}
	if after.Stage != before.Stage {
		t.Fatalf("stage changed on rejected advance: %q -> %q", before.Stage, after.Stage)
	}
	if len(after.DomainsDone) != len(before.DomainsDone) {
		t.Fatalf("domains changed on rejected advance: %v -> %v", before.DomainsDone, after.DomainsDone)
	}
	if after.UpdatedAtMS != before.UpdatedAtMS {
		t.Fatalf("updated_at_ms changed on rejected advance: %d -> %d", before.UpdatedAtMS, after.UpdatedAtMS)
	}
	if after.HasDomain(model.UpgradeDomainCoverage) {
		t.Fatal("rejected advance recorded its domain")
	}
}

func TestAdvanceRefusesToLeaveCompleted(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	if _, err := repository.Advance(ctx, "", model.UpgradeStageComplete); err != nil {
		t.Fatalf("advance complete: %v", err)
	}
	before, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get completed row: %v", err)
	}
	for _, stage := range []model.UpgradeStage{model.UpgradeStageVerify, model.UpgradeStageFailed, model.UpgradeStageComplete} {
		if _, err := repository.Advance(ctx, model.UpgradeDomainCoverage, stage); !errors.Is(err, upgradeoperation.ErrStageRegression) {
			t.Fatalf("advance complete -> %q error = %v, want ErrStageRegression", stage, err)
		}
	}
	stored, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Stage != model.UpgradeStageComplete || stored.Revision != before.Revision {
		t.Fatalf("stage = %q revision = %d, want complete/%d", stored.Stage, stored.Revision, before.Revision)
	}
	if stored.HasDomain(model.UpgradeDomainCoverage) {
		t.Fatal("rejected advance recorded its domain")
	}
}

func TestAdvanceIntoFailedKeepsCompletedDomains(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	if _, err := repository.Advance(ctx, model.UpgradeDomainUsageEvents, model.UpgradeStageVerify); err != nil {
		t.Fatalf("advance verify: %v", err)
	}
	failed, err := repository.Advance(ctx, "", model.UpgradeStageFailed)
	if err != nil {
		t.Fatalf("advance failed: %v", err)
	}
	if failed.Stage != model.UpgradeStageFailed || !failed.HasDomain(model.UpgradeDomainUsageEvents) {
		t.Fatalf("failed advance = %q %v", failed.Stage, failed.DomainsDone)
	}
	resumed, err := repository.Advance(ctx, model.UpgradeDomainCoverage, model.UpgradeStageVerify)
	if err != nil {
		t.Fatalf("resume after failure: %v", err)
	}
	if resumed.Stage != model.UpgradeStageVerify || !resumed.HasDomain(model.UpgradeDomainUsageEvents) {
		t.Fatalf("resume = %q %v", resumed.Stage, resumed.DomainsDone)
	}
}

func TestAdvanceRejectsUnknownDomain(t *testing.T) {
	repository, _ := newRepository(t)
	if _, err := repository.Advance(context.Background(), "unknown_domain", model.UpgradeStagePreflight); !errors.Is(err, upgradeoperation.ErrUnknownDomain) {
		t.Fatalf("unknown domain error = %v, want ErrUnknownDomain", err)
	}
}

func TestPutKeepsOneRowAndCreationTime(t *testing.T) {
	repository, db := newRepository(t)
	ctx := context.Background()
	created, err := repository.Advance(ctx, model.UpgradeDomainSourceCopy, model.UpgradeStagePreflight)
	if err != nil {
		t.Fatalf("advance preflight: %v", err)
	}
	if err := repository.Put(ctx, model.UpgradeOperation{
		Stage:          model.UpgradeStageCopyIn,
		DomainsDone:    []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainUsageEvents},
		SourceKind:     "legacy-manager",
		SourceVersion:  "v1.14.0",
		Reconciliation: nil,
		Revision:       created.Revision,
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	var rows int
	if err := db.QueryRowContext(ctx, `select count(*) from upgrade_operations`).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("upgrade_operations rows = %d, want 1", rows)
	}
	stored, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get after put: found=%t err=%v", found, err)
	}
	if stored.CreatedAtMS != created.CreatedAtMS {
		t.Fatalf("created_at_ms = %d, want preserved %d", stored.CreatedAtMS, created.CreatedAtMS)
	}
	if stored.SourceKind != "legacy-manager" || stored.SourceVersion != "v1.14.0" {
		t.Fatalf("source observation = %q/%q", stored.SourceKind, stored.SourceVersion)
	}
	if string(stored.Reconciliation) != "{}" {
		t.Fatalf("reconciliation = %s, want {}", stored.Reconciliation)
	}
}

// TestPutRefusesToMoveTheStageBackwards is the regression for M3: Put used to
// overwrite the stage and the domain list with whatever the caller passed.
func TestPutRefusesToMoveTheStageBackwards(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	if _, err := repository.Advance(ctx, model.UpgradeDomainUsageEvents, model.UpgradeStageVerify); err != nil {
		t.Fatalf("advance verify: %v", err)
	}
	before, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	err = repository.Put(ctx, model.UpgradeOperation{
		Stage:       model.UpgradeStagePreflight,
		DomainsDone: []string{model.UpgradeDomainUsageEvents},
		Revision:    before.Revision,
	})
	if !errors.Is(err, upgradeoperation.ErrStageRegression) {
		t.Fatalf("put error = %v, want ErrStageRegression", err)
	}
	after, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get after rejected put: %v", err)
	}
	if after.Stage != before.Stage || after.UpdatedAtMS != before.UpdatedAtMS {
		t.Fatalf("rejected put changed the row: %q/%d -> %q/%d", before.Stage, before.UpdatedAtMS, after.Stage, after.UpdatedAtMS)
	}
}

// TestPutRefusesToClearRecordedDomains is the second half of M3.
func TestPutRefusesToClearRecordedDomains(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	if _, err := repository.Advance(ctx, model.UpgradeDomainSourceCopy, model.UpgradeStageCopyIn); err != nil {
		t.Fatalf("advance copy_in: %v", err)
	}
	before, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get before rejected put: %v", err)
	}
	err = repository.Put(ctx, model.UpgradeOperation{
		Stage:       model.UpgradeStageReconcile,
		DomainsDone: []string{},
		Revision:    before.Revision,
	})
	if !errors.Is(err, upgradeoperation.ErrDomainCleared) {
		t.Fatalf("put error = %v, want ErrDomainCleared", err)
	}
	stored, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !stored.HasDomain(model.UpgradeDomainSourceCopy) {
		t.Fatalf("domains = %v, want source_copy retained", stored.DomainsDone)
	}
}

// TestPutNeverMovesUpdatedAtBackwards is N5: Put honoured any caller-supplied
// updated_at_ms that was not below created_at_ms, so a stale caller could make
// the row look older than the write it replaced.
func TestPutNeverMovesUpdatedAtBackwards(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	created, err := repository.Advance(ctx, model.UpgradeDomainSourceCopy, model.UpgradeStagePreflight)
	if err != nil {
		t.Fatalf("advance preflight: %v", err)
	}
	if err := repository.Put(ctx, model.UpgradeOperation{
		Stage:       model.UpgradeStageCopyIn,
		DomainsDone: []string{model.UpgradeDomainSourceCopy},
		UpdatedAtMS: created.UpdatedAtMS - 5_000,
		Revision:    created.Revision,
	}); err != nil {
		t.Fatalf("put with an older timestamp: %v", err)
	}
	first, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get after put: found=%t err=%v", found, err)
	}
	if first.UpdatedAtMS < created.UpdatedAtMS {
		t.Fatalf("updated_at_ms went backwards: %d -> %d", created.UpdatedAtMS, first.UpdatedAtMS)
	}

	if err := repository.Put(ctx, model.UpgradeOperation{
		Stage:       model.UpgradeStageReconcile,
		DomainsDone: []string{model.UpgradeDomainSourceCopy},
		UpdatedAtMS: 1,
		Revision:    first.Revision,
	}); err != nil {
		t.Fatalf("put with a stale timestamp: %v", err)
	}
	second, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get after second put: %v", err)
	}
	if second.UpdatedAtMS < first.UpdatedAtMS {
		t.Fatalf("updated_at_ms went backwards: %d -> %d", first.UpdatedAtMS, second.UpdatedAtMS)
	}
	if second.UpdatedAtMS < second.CreatedAtMS {
		t.Fatalf("updated_at_ms = %d is before created_at_ms = %d", second.UpdatedAtMS, second.CreatedAtMS)
	}
}

// TestPutOwnsUpdatedAtMS is the regression for the 2026-11-02 delta review
// Low-1: Put stored whatever updated_at_ms the caller proposed, so a unit
// mistake (MaxInt64 from a microsecond value) was accepted and, because Advance
// no longer resets the column, every later write stayed pinned to it. The
// repository owns the column: the write clock and the stored row are the only
// inputs.
func TestPutOwnsUpdatedAtMS(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	before := time.Now().UnixMilli()
	if err := repository.Put(ctx, model.UpgradeOperation{
		Stage:       model.UpgradeStagePreflight,
		UpdatedAtMS: math.MaxInt64,
	}); err != nil {
		t.Fatalf("put with an unbounded updated_at_ms: %v", err)
	}
	after := time.Now().UnixMilli()
	stored, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get after put: found=%t err=%v", found, err)
	}
	if stored.UpdatedAtMS < before || stored.UpdatedAtMS > after {
		t.Fatalf("updated_at_ms = %d, want the write clock in [%d, %d]", stored.UpdatedAtMS, before, after)
	}
	if stored.UpdatedAtMS != stored.CreatedAtMS {
		t.Fatalf("timestamps = created %d, updated %d, want equal after the first write", stored.CreatedAtMS, stored.UpdatedAtMS)
	}
	advanced, err := repository.Advance(ctx, model.UpgradeDomainSourceCopy, model.UpgradeStageCopyIn)
	if err != nil {
		t.Fatalf("advance after a proposed updated_at_ms: %v", err)
	}
	if advanced.UpdatedAtMS < stored.UpdatedAtMS {
		t.Fatalf("advance moved updated_at_ms backwards: %d -> %d", stored.UpdatedAtMS, advanced.UpdatedAtMS)
	}
	if advanced.UpdatedAtMS > time.Now().UnixMilli() {
		t.Fatalf("advanced updated_at_ms = %d is ahead of the clock", advanced.UpdatedAtMS)
	}
}

// TestAdvanceKeepsAFutureDatedRowReadable is the regression for the 2026-11-02
// review finding that Advance stamped the wall clock unconditionally: a row
// written while the host clock ran ahead (or restored from a later backup)
// advanced successfully and was then rejected by Get with ErrInvalidRow, which
// blocked status and every later advance.
func TestAdvanceKeepsAFutureDatedRowReadable(t *testing.T) {
	repository, db := newRepository(t)
	ctx := context.Background()
	future := time.Now().Add(time.Hour).UnixMilli()
	if _, err := db.ExecContext(ctx, `insert into upgrade_operations
		(id, stage, last_forward_stage, domains_done, reconciliation, manual_items, created_at_ms, updated_at_ms)
		values (1, ?, ?, '[]', '{}', '[]', ?, ?)`,
		string(model.UpgradeStagePreflight), string(model.UpgradeStagePreflight), future, future); err != nil {
		t.Fatalf("seed a future-dated row: %v", err)
	}
	before, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get seeded row: found=%t err=%v", found, err)
	}
	advanced, err := repository.Advance(ctx, model.UpgradeDomainSourceCopy, model.UpgradeStageCopyIn)
	if err != nil {
		t.Fatalf("advance a future-dated row: %v", err)
	}
	if advanced.UpdatedAtMS < before.UpdatedAtMS {
		t.Fatalf("advance moved updated_at_ms backwards: %d -> %d", before.UpdatedAtMS, advanced.UpdatedAtMS)
	}
	stored, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get after advance: found=%t err=%v", found, err)
	}
	if stored.UpdatedAtMS < before.UpdatedAtMS {
		t.Fatalf("updated_at_ms went backwards: %d -> %d", before.UpdatedAtMS, stored.UpdatedAtMS)
	}
	if stored.UpdatedAtMS < stored.CreatedAtMS {
		t.Fatalf("updated_at_ms = %d is before created_at_ms = %d", stored.UpdatedAtMS, stored.CreatedAtMS)
	}
	if stored.CreatedAtMS != future {
		t.Fatalf("created_at_ms = %d, want preserved %d", stored.CreatedAtMS, future)
	}
	if stored.Stage != model.UpgradeStageCopyIn || !stored.HasDomain(model.UpgradeDomainSourceCopy) {
		t.Fatalf("advanced row = %q %v", stored.Stage, stored.DomainsDone)
	}
}

// TestAdvanceKeepsACreatedOnlyRowReadable is the regression the 2026-11-02 delta
// review asked for (Low-2): the created_at_ms candidate in nextUpdatedAtMS had
// no coverage. A row with created_at_ms set and updated_at_ms still zero is
// valid, so Advance must keep it readable; dropping the created candidate makes
// Advance stamp the wall clock and write a row Get rejects.
func TestAdvanceKeepsACreatedOnlyRowReadable(t *testing.T) {
	repository, db := newRepository(t)
	ctx := context.Background()
	created := time.Now().Add(time.Hour).UnixMilli()
	if _, err := db.ExecContext(ctx, `insert into upgrade_operations
		(id, stage, last_forward_stage, domains_done, reconciliation, manual_items, created_at_ms, updated_at_ms)
		values (1, ?, ?, '[]', '{}', '[]', ?, 0)`,
		string(model.UpgradeStagePreflight), string(model.UpgradeStagePreflight), created); err != nil {
		t.Fatalf("seed a created-only row: %v", err)
	}
	seeded, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get seeded row: found=%t err=%v", found, err)
	}
	if seeded.CreatedAtMS != created || seeded.UpdatedAtMS != 0 {
		t.Fatalf("seeded timestamps = %d/%d, want %d/0", seeded.CreatedAtMS, seeded.UpdatedAtMS, created)
	}
	advanced, err := repository.Advance(ctx, model.UpgradeDomainSourceCopy, model.UpgradeStageCopyIn)
	if err != nil {
		t.Fatalf("advance a created-only row: %v", err)
	}
	if advanced.UpdatedAtMS < advanced.CreatedAtMS {
		t.Fatalf("advanced updated_at_ms = %d is before created_at_ms = %d", advanced.UpdatedAtMS, advanced.CreatedAtMS)
	}
	stored, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get after advance: found=%t err=%v", found, err)
	}
	if stored.CreatedAtMS != created {
		t.Fatalf("created_at_ms = %d, want preserved %d", stored.CreatedAtMS, created)
	}
	if stored.UpdatedAtMS < stored.CreatedAtMS {
		t.Fatalf("updated_at_ms = %d is before created_at_ms = %d", stored.UpdatedAtMS, stored.CreatedAtMS)
	}
	if stored.Stage != model.UpgradeStageCopyIn || !stored.HasDomain(model.UpgradeDomainSourceCopy) {
		t.Fatalf("advanced row = %q %v", stored.Stage, stored.DomainsDone)
	}
}

// TestPutThenAdvanceNeverMovesUpdatedAtBackwards covers the same finding through
// the public API: Put stored a caller-supplied time ahead of the clock, and
// Advance then replaced it with the wall clock.
func TestPutThenAdvanceNeverMovesUpdatedAtBackwards(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	if err := repository.Put(ctx, model.UpgradeOperation{
		Stage:       model.UpgradeStagePreflight,
		UpdatedAtMS: time.Now().Add(time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("put with a future timestamp: %v", err)
	}
	before, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get after put: found=%t err=%v", found, err)
	}
	advanced, err := repository.Advance(ctx, model.UpgradeDomainSourceCopy, model.UpgradeStageCopyIn)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if advanced.UpdatedAtMS < before.UpdatedAtMS {
		t.Fatalf("advance moved updated_at_ms backwards: %d -> %d", before.UpdatedAtMS, advanced.UpdatedAtMS)
	}
	stored, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get after advance: %v", err)
	}
	if stored.UpdatedAtMS < before.UpdatedAtMS {
		t.Fatalf("updated_at_ms went backwards: %d -> %d", before.UpdatedAtMS, stored.UpdatedAtMS)
	}
}

// TestPutReportsTypedInputErrors is the regression for the review finding that
// Put returned bare model errors: a caller could classify a bad stage or domain
// from Advance with errors.Is but not from Put.
func TestPutReportsTypedInputErrors(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	if err := repository.Put(ctx, model.UpgradeOperation{Stage: "unknown_stage"}); !errors.Is(err, upgradeoperation.ErrUnknownStage) {
		t.Fatalf("put with an unknown stage error = %v, want ErrUnknownStage", err)
	}
	err := repository.Put(ctx, model.UpgradeOperation{
		Stage:       model.UpgradeStagePreflight,
		DomainsDone: []string{"unknown_domain"},
	})
	if !errors.Is(err, upgradeoperation.ErrUnknownDomain) {
		t.Fatalf("put with an unknown domain error = %v, want ErrUnknownDomain", err)
	}
	if _, found, getErr := repository.Get(ctx); getErr != nil || found {
		t.Fatalf("a rejected put wrote a row: found=%t err=%v", found, getErr)
	}
}

func TestPutMayOnlyAppendDomainsAndAdvance(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	advanced, err := repository.Advance(ctx, model.UpgradeDomainSourceCopy, model.UpgradeStagePreflight)
	if err != nil {
		t.Fatalf("advance preflight: %v", err)
	}
	if err := repository.Put(ctx, model.UpgradeOperation{
		Stage:       model.UpgradeStageCopyIn,
		DomainsDone: []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainUsageEvents},
		Revision:    advanced.Revision,
	}); err != nil {
		t.Fatalf("forward put: %v", err)
	}
	stored, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("stage = %q, want copy_in", stored.Stage)
	}
	if !stored.HasDomain(model.UpgradeDomainUsageEvents) {
		t.Fatalf("domains = %v, want usage_events appended", stored.DomainsDone)
	}
}

// TestFirstPutClaimsRevisionZeroOnce covers competing first inserts: both
// callers read "no row" (revision 0), and only the first may commit. The second
// must conflict instead of overwriting the first observation.
func TestFirstPutClaimsRevisionZeroOnce(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	if err := repository.Put(ctx, model.UpgradeOperation{
		Stage:      model.UpgradeStagePreflight,
		SourceKind: "first",
	}); err != nil {
		t.Fatalf("first put: %v", err)
	}
	err := repository.Put(ctx, model.UpgradeOperation{
		Stage:      model.UpgradeStagePreflight,
		SourceKind: "second",
	})
	if !errors.Is(err, upgradeoperation.ErrConflict) {
		t.Fatalf("second first insert error = %v, want ErrConflict", err)
	}
	stored, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get after competing inserts: found=%t err=%v", found, err)
	}
	if stored.SourceKind != "first" || stored.Revision != 1 {
		t.Fatalf("stored row = source %q revision %d, want first/1", stored.SourceKind, stored.Revision)
	}
}

// TestPutRejectsAStaleSnapshot is the R2 regression: two readers hold the same
// stage, the second commits newer evidence, and the first then replays its old
// snapshot. Without the revision token the stale write replaced fingerprints,
// manifest and reconciliation while updated_at_ms still looked monotonic.
func TestPutRejectsAStaleSnapshot(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	if _, err := repository.Advance(ctx, model.UpgradeDomainSourceCopy, model.UpgradeStagePreflight); err != nil {
		t.Fatalf("advance preflight: %v", err)
	}
	stale, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get stale reader: found=%t err=%v", found, err)
	}
	stale.Stage = model.UpgradeStageCopyIn
	stale.SourceFingerprintAfter = "sha256:stale"
	stale.BackupManifestSHA256 = "sha256:stale-manifest"
	stale.Reconciliation = json.RawMessage(`{"reader":"stale"}`)

	fresh := stale
	fresh.SourceFingerprintAfter = "sha256:fresh"
	fresh.BackupManifestSHA256 = "sha256:fresh-manifest"
	fresh.Reconciliation = json.RawMessage(`{"reader":"fresh"}`)
	if err := repository.Put(ctx, fresh); err != nil {
		t.Fatalf("fresh put: %v", err)
	}
	committed, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get committed row: found=%t err=%v", found, err)
	}
	if err := repository.Put(ctx, stale); !errors.Is(err, upgradeoperation.ErrConflict) {
		t.Fatalf("stale put error = %v, want ErrConflict", err)
	}
	after, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get after stale put: found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(committed, after) {
		t.Fatalf("stale put changed the row:\n before %+v\n after  %+v", committed, after)
	}
	if after.SourceFingerprintAfter != "sha256:fresh" || after.BackupManifestSHA256 != "sha256:fresh-manifest" {
		t.Fatalf("stale put replaced evidence: %q/%q", after.SourceFingerprintAfter, after.BackupManifestSHA256)
	}
	if string(after.Reconciliation) != `{"reader":"fresh"}` {
		t.Fatalf("stale put replaced reconciliation: %s", after.Reconciliation)
	}
}

// TestPutAndAdvanceShareOneRevisionToken covers the R2 cross-API case: an
// Advance commits between a caller's Get and Put, so the token the caller holds
// is stale even though the stage it wants to write would still be forward.
func TestPutAndAdvanceShareOneRevisionToken(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	if _, err := repository.Advance(ctx, "", model.UpgradeStagePreflight); err != nil {
		t.Fatalf("advance preflight: %v", err)
	}
	observed, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get observed row: %v", err)
	}
	advanced, err := repository.Advance(ctx, model.UpgradeDomainSourceCopy, model.UpgradeStageCopyIn)
	if err != nil {
		t.Fatalf("advance copy_in: %v", err)
	}
	if advanced.Revision != observed.Revision+1 {
		t.Fatalf("advanced revision = %d, want %d", advanced.Revision, observed.Revision+1)
	}
	err = repository.Put(ctx, model.UpgradeOperation{
		Stage:       model.UpgradeStageReconcile,
		DomainsDone: []string{model.UpgradeDomainSourceCopy},
		Revision:    observed.Revision,
	})
	if !errors.Is(err, upgradeoperation.ErrConflict) {
		t.Fatalf("put after advance error = %v, want ErrConflict", err)
	}
	stored, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get after conflicting put: %v", err)
	}
	if stored.Stage != model.UpgradeStageCopyIn || stored.Revision != advanced.Revision {
		t.Fatalf("row = %q/%d, want copy_in/%d", stored.Stage, stored.Revision, advanced.Revision)
	}
}

// TestRevisionIncrementsWithinTheSameMillisecond covers the R2 token choice: a
// stored row whose clock is ahead keeps updated_at_ms constant for every write,
// so the timestamp cannot be the token; revision must still increase.
func TestRevisionIncrementsWithinTheSameMillisecond(t *testing.T) {
	repository, db := newRepository(t)
	ctx := context.Background()
	future := time.Now().Add(time.Hour).UnixMilli()
	if _, err := db.ExecContext(ctx, `insert into upgrade_operations
		(id, stage, last_forward_stage, domains_done, reconciliation, manual_items, created_at_ms, updated_at_ms)
		values (1, ?, ?, '[]', '{}', '[]', ?, ?)`,
		string(model.UpgradeStagePreflight), string(model.UpgradeStagePreflight), future, future); err != nil {
		t.Fatalf("seed a future-dated row: %v", err)
	}
	observed, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get seeded row: %v", err)
	}
	if observed.Revision != 0 {
		t.Fatalf("seeded revision = %d, want 0", observed.Revision)
	}
	if err := repository.Put(ctx, model.UpgradeOperation{
		Stage:    model.UpgradeStageCopyIn,
		Revision: observed.Revision,
	}); err != nil {
		t.Fatalf("first put: %v", err)
	}
	first, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get after first put: %v", err)
	}
	if err := repository.Put(ctx, model.UpgradeOperation{
		Stage:    model.UpgradeStageReconcile,
		Revision: first.Revision,
	}); err != nil {
		t.Fatalf("second put: %v", err)
	}
	second, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get after second put: %v", err)
	}
	advanced, err := repository.Advance(ctx, model.UpgradeDomainUsageEvents, model.UpgradeStageCPAImport)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	revisions := []int64{observed.Revision, first.Revision, second.Revision, advanced.Revision}
	want := []int64{0, 1, 2, 3}
	if !reflect.DeepEqual(revisions, want) {
		t.Fatalf("revisions = %v, want %v", revisions, want)
	}
	for index, stored := range []model.UpgradeOperation{first, second} {
		if stored.UpdatedAtMS != future {
			t.Fatalf("write %d updated_at_ms = %d, want unchanged %d", index, stored.UpdatedAtMS, future)
		}
	}
	if advanced.UpdatedAtMS != future {
		t.Fatalf("advance updated_at_ms = %d, want unchanged %d", advanced.UpdatedAtMS, future)
	}
}

// TestFailedKeepsHighWaterAndRefusesLowerResume is the R3 regression:
// verify -> failed -> copy_in used to succeed because failed erased the
// position the operation had reached.
func TestFailedKeepsHighWaterAndRefusesLowerResume(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	if _, err := repository.Advance(ctx, model.UpgradeDomainUsageEvents, model.UpgradeStageVerify); err != nil {
		t.Fatalf("advance verify: %v", err)
	}
	failed, err := repository.Advance(ctx, "", model.UpgradeStageFailed)
	if err != nil {
		t.Fatalf("advance failed: %v", err)
	}
	if failed.Stage != model.UpgradeStageFailed || failed.LastForwardStage != model.UpgradeStageVerify {
		t.Fatalf("failed row = %q high-water %q, want failed/verify", failed.Stage, failed.LastForwardStage)
	}
	before, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get before resume: %v", err)
	}
	if _, err := repository.Advance(ctx, "", model.UpgradeStageCopyIn); !errors.Is(err, upgradeoperation.ErrStageRegression) {
		t.Fatalf("resume below high-water error = %v, want ErrStageRegression", err)
	}
	unchanged, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get after refused resume: %v", err)
	}
	if !reflect.DeepEqual(before, unchanged) {
		t.Fatalf("refused resume changed the row:\n before %+v\n after  %+v", before, unchanged)
	}
	resumed, err := repository.Advance(ctx, model.UpgradeDomainCoverage, model.UpgradeStageVerify)
	if err != nil {
		t.Fatalf("resume at high-water: %v", err)
	}
	if resumed.Stage != model.UpgradeStageVerify || resumed.LastForwardStage != model.UpgradeStageVerify {
		t.Fatalf("resumed row = %q high-water %q, want verify/verify", resumed.Stage, resumed.LastForwardStage)
	}
	if resumed.Revision != before.Revision+1 {
		t.Fatalf("resumed revision = %d, want %d", resumed.Revision, before.Revision+1)
	}
}

// TestPutResumesFailedAtOrAboveHighWater is the R3 regression through Put: the
// same rule must hold for evidence writes, and a stale token must not bypass it.
func TestPutResumesFailedAtOrAboveHighWater(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	if _, err := repository.Advance(ctx, model.UpgradeDomainUsageEvents, model.UpgradeStageVerify); err != nil {
		t.Fatalf("advance verify: %v", err)
	}
	parked, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get verified row: %v", err)
	}
	parked.Stage = model.UpgradeStageFailed
	if err := repository.Put(ctx, parked); err != nil {
		t.Fatalf("put failed: %v", err)
	}
	stored, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get failed row: %v", err)
	}
	if stored.Stage != model.UpgradeStageFailed || stored.LastForwardStage != model.UpgradeStageVerify {
		t.Fatalf("failed row = %q high-water %q", stored.Stage, stored.LastForwardStage)
	}
	lower := stored
	lower.Stage = model.UpgradeStageCopyIn
	if err := repository.Put(ctx, lower); !errors.Is(err, upgradeoperation.ErrStageRegression) {
		t.Fatalf("put below high-water error = %v, want ErrStageRegression", err)
	}
	unchanged, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get after refused put: %v", err)
	}
	if !reflect.DeepEqual(stored, unchanged) {
		t.Fatalf("refused put changed the row:\n before %+v\n after  %+v", stored, unchanged)
	}
	resume := stored
	resume.Stage = model.UpgradeStageVerify
	if err := repository.Put(ctx, resume); err != nil {
		t.Fatalf("put at high-water: %v", err)
	}
	resumed, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get resumed row: %v", err)
	}
	if resumed.Stage != model.UpgradeStageVerify || resumed.LastForwardStage != model.UpgradeStageVerify {
		t.Fatalf("resumed row = %q high-water %q", resumed.Stage, resumed.LastForwardStage)
	}
	if resumed.Revision != stored.Revision+1 {
		t.Fatalf("resumed revision = %d, want %d", resumed.Revision, stored.Revision+1)
	}
}

// TestPutRefusesToLeaveCompleted closes the other end of R3: complete is
// terminal for both write APIs, including complete -> failed.
func TestPutRefusesToLeaveCompleted(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	if _, err := repository.Advance(ctx, "", model.UpgradeStageComplete); err != nil {
		t.Fatalf("advance complete: %v", err)
	}
	complete, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get complete row: %v", err)
	}
	for _, stage := range []model.UpgradeStage{model.UpgradeStageFailed, model.UpgradeStageVerify} {
		candidate := complete
		candidate.Stage = stage
		if err := repository.Put(ctx, candidate); !errors.Is(err, upgradeoperation.ErrStageRegression) {
			t.Fatalf("put complete -> %q error = %v, want ErrStageRegression", stage, err)
		}
	}
	after, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get after refused puts: %v", err)
	}
	if !reflect.DeepEqual(complete, after) {
		t.Fatalf("refused put changed the completed row:\n before %+v\n after  %+v", complete, after)
	}
}

// TestPutRejectsNonObjectReconciliation is the R7 regression: decoding into a
// map accepted JSON null, so the stored row broke the "JSON object" contract.
func TestPutRejectsNonObjectReconciliation(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	for _, raw := range []string{"null", "[]", `"text"`, "1", "{broken"} {
		err := repository.Put(ctx, model.UpgradeOperation{
			Stage:          model.UpgradeStagePreflight,
			Reconciliation: json.RawMessage(raw),
		})
		if err == nil {
			t.Fatalf("put reconciliation %s was accepted", raw)
		}
	}
	if _, found, err := repository.Get(ctx); err != nil || found {
		t.Fatalf("rejected put wrote a row: found=%t err=%v", found, err)
	}
	if err := repository.Put(ctx, model.UpgradeOperation{
		Stage:          model.UpgradeStagePreflight,
		Reconciliation: json.RawMessage(`{"checked":true}`),
	}); err != nil {
		t.Fatalf("put object reconciliation: %v", err)
	}
	stored, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(stored.Reconciliation) != `{"checked":true}` {
		t.Fatalf("reconciliation = %s", stored.Reconciliation)
	}
}

// TestGetRejectsStoredNonObjectReconciliation covers the read side of R7: a row
// written by an older build with reconciliation = null must fail closed.
func TestGetRejectsStoredNonObjectReconciliation(t *testing.T) {
	for _, raw := range []string{"null", "[1,2]"} {
		t.Run(raw, func(t *testing.T) {
			repository, db := newRepository(t)
			ctx := context.Background()
			if _, err := db.ExecContext(ctx, `insert into upgrade_operations
				(id, stage, last_forward_stage, domains_done, reconciliation, manual_items, created_at_ms, updated_at_ms)
				values (1, ?, ?, '[]', ?, '[]', 1, 1)`,
				string(model.UpgradeStagePreflight), string(model.UpgradeStagePreflight), raw); err != nil {
				t.Fatalf("seed corrupted row: %v", err)
			}
			if _, _, err := repository.Get(ctx); !errors.Is(err, upgradeoperation.ErrInvalidRow) {
				t.Fatalf("get corrupted reconciliation error = %v, want ErrInvalidRow", err)
			}
		})
	}
}

// TestPutRejectsDuplicateDomainsBeforeNormalizing is the R9 regression: Put
// normalized before validating, so a duplicate domain was silently deduplicated
// and the write committed.
func TestPutRejectsDuplicateDomainsBeforeNormalizing(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	err := repository.Put(ctx, model.UpgradeOperation{
		Stage:       model.UpgradeStagePreflight,
		DomainsDone: []string{model.UpgradeDomainPricing, model.UpgradeDomainPricing},
	})
	if err == nil {
		t.Fatal("put with duplicate domains was accepted")
	}
	if _, found, getErr := repository.Get(ctx); getErr != nil || found {
		t.Fatalf("rejected put wrote a row: found=%t err=%v", found, getErr)
	}
	if _, err := repository.Advance(ctx, model.UpgradeDomainUsageEvents, model.UpgradeStageCopyIn); err != nil {
		t.Fatalf("advance: %v", err)
	}
	before, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get before rejected put: %v", err)
	}
	err = repository.Put(ctx, model.UpgradeOperation{
		Stage:       model.UpgradeStageReconcile,
		DomainsDone: []string{model.UpgradeDomainUsageEvents, model.UpgradeDomainUsageEvents},
		Revision:    before.Revision,
	})
	if err == nil {
		t.Fatal("put with duplicate domains was accepted on an existing row")
	}
	after, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get after rejected put: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected put changed the row:\n before %+v\n after  %+v", before, after)
	}
}

// TestGetRejectsStoredDuplicateDomains is the read half of R9: Normalize used to
// deduplicate a corrupted row before Get returned it.
func TestGetRejectsStoredDuplicateDomains(t *testing.T) {
	repository, db := newRepository(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `insert into upgrade_operations
		(id, stage, last_forward_stage, domains_done, reconciliation, manual_items, created_at_ms, updated_at_ms)
		values (1, ?, ?, '["pricing","pricing"]', '{}', '[]', 1, 1)`,
		string(model.UpgradeStagePreflight), string(model.UpgradeStagePreflight)); err != nil {
		t.Fatalf("seed duplicate-domain row: %v", err)
	}
	if _, _, err := repository.Get(ctx); !errors.Is(err, upgradeoperation.ErrInvalidRow) {
		t.Fatalf("get stored duplicate domains error = %v, want ErrInvalidRow", err)
	}
}

// TestAdvanceRepeatsDuplicateDomainIdempotently keeps the R9 contract for
// Advance: re-recording a domain is the normal idempotent operation, not an
// input error.
func TestAdvanceRepeatsDuplicateDomainIdempotently(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	first, err := repository.Advance(ctx, model.UpgradeDomainUsageEvents, model.UpgradeStageCopyIn)
	if err != nil {
		t.Fatalf("advance copy_in: %v", err)
	}
	second, err := repository.Advance(ctx, model.UpgradeDomainUsageEvents, model.UpgradeStageReconcile)
	if err != nil {
		t.Fatalf("re-advance usage_events: %v", err)
	}
	occurrences := 0
	for _, domain := range second.DomainsDone {
		if domain == model.UpgradeDomainUsageEvents {
			occurrences++
		}
	}
	if occurrences != 1 {
		t.Fatalf("domains = %v, want usage_events recorded once", second.DomainsDone)
	}
	if second.Revision != first.Revision+1 {
		t.Fatalf("repeated advance revision = %d, want %d", second.Revision, first.Revision+1)
	}
}

// TestAdvanceIntoCompleteThenFreezesTheRow is the R10 regression for Advance:
// complete was compared after the same-stage shortcut, so a completed row could
// still append a domain, move its revision and rewrite its timestamp. The first
// entry into complete succeeds; afterwards only an exactly identical repeat is
// accepted, and it writes nothing.
func TestAdvanceIntoCompleteThenFreezesTheRow(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	if _, err := repository.Advance(ctx, model.UpgradeDomainUsageEvents, model.UpgradeStageVerify); err != nil {
		t.Fatalf("advance verify: %v", err)
	}
	if _, err := repository.Advance(ctx, "", model.UpgradeStageComplete); err != nil {
		t.Fatalf("first advance into complete: %v", err)
	}
	complete, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get completed row: found=%t err=%v", found, err)
	}
	if complete.Stage != model.UpgradeStageComplete {
		t.Fatalf("stage = %q, want complete", complete.Stage)
	}
	if _, err := repository.Advance(ctx, model.UpgradeDomainCoverage, model.UpgradeStageComplete); !errors.Is(err, upgradeoperation.ErrStageRegression) {
		t.Fatalf("advance complete -> complete with a new domain error = %v, want ErrStageRegression", err)
	}
	assertStoredOperation(t, repository, complete)

	if _, err := repository.Advance(ctx, "", model.UpgradeStageFailed); !errors.Is(err, upgradeoperation.ErrStageRegression) {
		t.Fatalf("advance complete -> failed error = %v, want ErrStageRegression", err)
	}
	assertStoredOperation(t, repository, complete)

	repeat, err := repository.Advance(ctx, model.UpgradeDomainUsageEvents, model.UpgradeStageComplete)
	if err != nil {
		t.Fatalf("identical repeated advance into complete: %v", err)
	}
	if !reflect.DeepEqual(complete, repeat) {
		t.Fatalf("identical repeat changed the completed row:\n want %+v\n got  %+v", complete, repeat)
	}
	assertStoredOperation(t, repository, complete)

	if _, err := repository.Advance(ctx, "", model.UpgradeStageComplete); err != nil {
		t.Fatalf("identical completed advance without a domain: %v", err)
	}
	assertStoredOperation(t, repository, complete)
}

// TestFirstAdvanceIntoCompleteCreatesTheRow keeps the other half of the R10
// contract: refusal applies to a stored complete row, not to the write that
// creates the operation in complete.
func TestFirstAdvanceIntoCompleteCreatesTheRow(t *testing.T) {
	repository, _ := newRepository(t)
	created, err := repository.Advance(context.Background(), "", model.UpgradeStageComplete)
	if err != nil {
		t.Fatalf("first advance into complete: %v", err)
	}
	if created.Stage != model.UpgradeStageComplete || created.LastForwardStage != model.UpgradeStageComplete {
		t.Fatalf("created row = %q high-water %q, want complete/complete", created.Stage, created.LastForwardStage)
	}
	if created.Revision != 1 {
		t.Fatalf("created row revision = %d, want 1", created.Revision)
	}
	assertStoredOperation(t, repository, created)
}

// TestPutCannotChangeAStoredCompleteRow is the R10 regression for Put: a
// same-stage Put replaced observations on a completed row because the stage
// comparison accepted complete -> complete.
func TestPutCannotChangeAStoredCompleteRow(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	if _, err := repository.Advance(ctx, "", model.UpgradeStageComplete); err != nil {
		t.Fatalf("advance complete: %v", err)
	}
	complete, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get completed row: found=%t err=%v", found, err)
	}
	changedManifest := complete
	changedManifest.BackupManifestSHA256 = "sha256:later"
	changedManifest.ManualItems = []string{"confirm the archive copy"}
	if err := repository.Put(ctx, changedManifest); !errors.Is(err, upgradeoperation.ErrStageRegression) {
		t.Fatalf("put changing a completed row error = %v, want ErrStageRegression", err)
	}
	assertStoredOperation(t, repository, complete)

	changedDomains := complete
	changedDomains.DomainsDone = []string{model.UpgradeDomainCoverage}
	if err := repository.Put(ctx, changedDomains); !errors.Is(err, upgradeoperation.ErrStageRegression) {
		t.Fatalf("put adding a domain to a completed row error = %v, want ErrStageRegression", err)
	}
	assertStoredOperation(t, repository, complete)

	// An exactly identical repeat is the one accepted call, and it must not
	// bump the revision or the timestamp.
	if err := repository.Put(ctx, complete); err != nil {
		t.Fatalf("identical put on a completed row: %v", err)
	}
	assertStoredOperation(t, repository, complete)
}

// TestPutIntoCompleteCreatesTheRowOnce covers the creation side of the R10 Put
// rule: the first write may store complete, the next may only repeat it.
func TestPutIntoCompleteCreatesTheRowOnce(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	if err := repository.Put(ctx, model.UpgradeOperation{
		Stage:                model.UpgradeStageComplete,
		DomainsDone:          []string{model.UpgradeDomainCoverage},
		BackupManifestSHA256: "sha256:first",
		Reconciliation:       json.RawMessage(`{"checked":true}`),
		ManualItems:          []string{"hand over the copied database"},
	}); err != nil {
		t.Fatalf("first put into complete: %v", err)
	}
	complete, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get completed row: found=%t err=%v", found, err)
	}
	if complete.Stage != model.UpgradeStageComplete || !complete.HasDomain(model.UpgradeDomainCoverage) {
		t.Fatalf("completed row = %q %v", complete.Stage, complete.DomainsDone)
	}
	changed := complete
	changed.BackupManifestSHA256 = "sha256:second"
	if err := repository.Put(ctx, changed); !errors.Is(err, upgradeoperation.ErrStageRegression) {
		t.Fatalf("second put into complete error = %v, want ErrStageRegression", err)
	}
	assertStoredOperation(t, repository, complete)
}

// TestGetRejectsCorruptedStoredJSON is the R11 regression: defaults belong to
// construction and Put input only. An existing row must hold an actual JSON
// string array and an actual JSON object, so empty text and JSON null are
// corruption rather than an empty list or an empty reconciliation.
func TestGetRejectsCorruptedStoredJSON(t *testing.T) {
	cases := []struct {
		name           string
		domains        string
		manualItems    string
		reconciliation string
	}{
		{name: "domains_null", domains: "null", manualItems: "[]", reconciliation: "{}"},
		{name: "domains_empty", domains: "", manualItems: "[]", reconciliation: "{}"},
		{name: "domains_object", domains: "{}", manualItems: "[]", reconciliation: "{}"},
		{name: "domains_non_string_element", domains: `["pricing",1]`, manualItems: "[]", reconciliation: "{}"},
		{name: "manual_items_null", domains: "[]", manualItems: "null", reconciliation: "{}"},
		{name: "manual_items_empty", domains: "[]", manualItems: "", reconciliation: "{}"},
		{name: "manual_items_non_string_element", domains: "[]", manualItems: `["item",true]`, reconciliation: "{}"},
		{name: "reconciliation_empty", domains: "[]", manualItems: "[]", reconciliation: ""},
		{name: "reconciliation_null", domains: "[]", manualItems: "[]", reconciliation: "null"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			repository, db := newRepository(t)
			ctx := context.Background()
			if _, err := db.ExecContext(ctx, `insert into upgrade_operations
				(id, stage, last_forward_stage, domains_done, reconciliation, manual_items, created_at_ms, updated_at_ms)
				values (1, ?, ?, ?, ?, ?, 5, 5)`,
				string(model.UpgradeStagePreflight), string(model.UpgradeStagePreflight),
				testCase.domains, testCase.reconciliation, testCase.manualItems); err != nil {
				t.Fatalf("seed corrupted row: %v", err)
			}
			before := readRawOperation(t, db)
			if _, _, err := repository.Get(ctx); !errors.Is(err, upgradeoperation.ErrInvalidRow) {
				t.Fatalf("get corrupted row error = %v, want ErrInvalidRow", err)
			}
			if _, err := repository.Advance(ctx, model.UpgradeDomainUsageEvents, model.UpgradeStageCopyIn); !errors.Is(err, upgradeoperation.ErrInvalidRow) {
				t.Fatalf("advance on a corrupted row error = %v, want ErrInvalidRow", err)
			}
			if err := repository.Put(ctx, model.UpgradeOperation{
				Stage:       model.UpgradeStageCopyIn,
				DomainsDone: []string{model.UpgradeDomainUsageEvents},
			}); !errors.Is(err, upgradeoperation.ErrInvalidRow) {
				t.Fatalf("put on a corrupted row error = %v, want ErrInvalidRow", err)
			}
			if after := readRawOperation(t, db); after != before {
				t.Fatalf("corrupted row was written:\n before %s\n after  %s", before, after)
			}
		})
	}
}

// TestGetAcceptsStoredEmptyJSONAndEmptyPut keeps the non-corrupted side of R11:
// the schema defaults still read, and Put input may still omit the fields.
func TestGetAcceptsStoredEmptyJSONAndEmptyPut(t *testing.T) {
	repository, db := newRepository(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `insert into upgrade_operations
		(id, stage, last_forward_stage, domains_done, reconciliation, manual_items, created_at_ms, updated_at_ms)
		values (1, ?, ?, '[]', '{}', '[]', 5, 5)`,
		string(model.UpgradeStagePreflight), string(model.UpgradeStagePreflight)); err != nil {
		t.Fatalf("seed default row: %v", err)
	}
	stored, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get default row: found=%t err=%v", found, err)
	}
	if len(stored.DomainsDone) != 0 || len(stored.ManualItems) != 0 || string(stored.Reconciliation) != "{}" {
		t.Fatalf("default row = %v %v %s", stored.DomainsDone, stored.ManualItems, stored.Reconciliation)
	}
	if err := repository.Put(ctx, model.UpgradeOperation{
		Stage:    model.UpgradeStageCopyIn,
		Revision: stored.Revision,
	}); err != nil {
		t.Fatalf("put without domains, manual items or reconciliation: %v", err)
	}
	after, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get row after empty put: found=%t err=%v", found, err)
	}
	if after.Stage != model.UpgradeStageCopyIn || len(after.DomainsDone) != 0 || string(after.Reconciliation) != "{}" {
		t.Fatalf("row after empty put = %q %v %s", after.Stage, after.DomainsDone, after.Reconciliation)
	}
}

// assertStoredOperation fails unless the stored row equals want field by field,
// including revision and timestamps.
func assertStoredOperation(t *testing.T, repository *upgradeoperation.Repository, want model.UpgradeOperation) {
	t.Helper()
	got, found, err := repository.Get(context.Background())
	if err != nil || !found {
		t.Fatalf("get stored row: found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("stored row changed:\n want %+v\n got  %+v", want, got)
	}
}

// readRawOperation renders the whole row as one string so a test can prove that
// a rejected write left every column, including revision and timestamps, alone
// without going through Get (which rejects the corrupted rows under test).
func readRawOperation(t *testing.T, db *sql.DB) string {
	t.Helper()
	var row string
	if err := db.QueryRow(`select quote(stage) || '|' || quote(last_forward_stage) || '|' || quote(revision) ||
		'|' || quote(domains_done) || '|' || quote(source_kind) || '|' || quote(source_version) ||
		'|' || quote(source_fingerprint_before) || '|' || quote(source_fingerprint_after) ||
		'|' || quote(backup_manifest_sha256) || '|' || quote(reconciliation) ||
		'|' || quote(manual_items) || '|' || quote(error_code) || '|' || quote(error_detail) ||
		'|' || quote(created_at_ms) || '|' || quote(updated_at_ms)
		from upgrade_operations where id = 1`).Scan(&row); err != nil {
		t.Fatalf("read raw row: %v", err)
	}
	return row
}
