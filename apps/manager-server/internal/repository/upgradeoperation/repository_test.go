package upgradeoperation_test

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
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

// rollbackEvidence is a complete observation: every field a verdict rests on.
func rollbackEvidence() model.RollbackEvidence {
	return model.RollbackEvidence{
		Image:             "seakee/cpa-manager-plus:v1.14.7",
		StatusURL:         "http://127.0.0.1:18317/health",
		SourceFingerprint: "observed-after-the-v1-restart",
		ObservedAtMS:      1_700_000_000_000,
	}
}

// recordedCopyIn leaves the row the copy-in would have recorded.
func recordedCopyIn(t *testing.T, repository *upgradeoperation.Repository) model.UpgradeOperation {
	t.Helper()
	recorded, err := repository.Advance(context.Background(), model.UpgradeDomainSourceCopy, model.UpgradeStageCopyIn)
	if err != nil {
		t.Fatalf("advance to copy_in: %v", err)
	}
	recorded.SourceKind = "upgraded-legacy"
	recorded.SourceVersion = "v1.14.7"
	recorded.SourceRoot = "/srv/cpamp/data"
	recorded.SourceFingerprintBefore = "before"
	recorded.SourceFingerprintAfter = "after"
	recorded.BackupManifestSHA256 = "manifest"
	if err := repository.Put(context.Background(), recorded); err != nil {
		t.Fatalf("record provenance: %v", err)
	}
	return recorded
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
	if _, err := repository.Advance(ctx, model.UpgradeDomainCoverage, model.UpgradeStageVerify); !errors.Is(err, upgradeoperation.ErrStageRegression) {
		t.Fatalf("advance after complete error = %v, want ErrStageRegression", err)
	}
	stored, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Stage != model.UpgradeStageComplete {
		t.Fatalf("stage = %q, want complete", stored.Stage)
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
	err := repository.Put(ctx, model.UpgradeOperation{
		Stage:       model.UpgradeStageReconcile,
		DomainsDone: []string{},
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
		(id, stage, domains_done, reconciliation, manual_items, created_at_ms, updated_at_ms)
		values (1, ?, '[]', '{}', '[]', ?, ?)`,
		string(model.UpgradeStagePreflight), future, future); err != nil {
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
		(id, stage, domains_done, reconciliation, manual_items, created_at_ms, updated_at_ms)
		values (1, ?, '[]', '{}', '[]', ?, 0)`,
		string(model.UpgradeStagePreflight), created); err != nil {
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
	if _, err := repository.Advance(ctx, model.UpgradeDomainSourceCopy, model.UpgradeStagePreflight); err != nil {
		t.Fatalf("advance preflight: %v", err)
	}
	if err := repository.Put(ctx, model.UpgradeOperation{
		Stage:       model.UpgradeStageCopyIn,
		DomainsDone: []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainUsageEvents},
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

// TestRecordRollbackWritesTheVerdictAndItsEvidence proves the stage and the
// observation land together and that provenance is not rewritten.
func TestRecordRollbackWritesTheVerdictAndItsEvidence(t *testing.T) {
	ctx := context.Background()
	repository, _ := newRepository(t)
	before := recordedCopyIn(t, repository)

	evidence := rollbackEvidence()
	recorded, err := repository.RecordRollback(ctx, evidence)
	if err != nil {
		t.Fatalf("record rollback: %v", err)
	}
	if recorded.Stage != model.UpgradeStageRolledBack {
		t.Fatalf("stage = %q, want %q", recorded.Stage, model.UpgradeStageRolledBack)
	}
	if recorded.Rollback == nil || *recorded.Rollback != evidence {
		t.Fatalf("returned evidence = %+v, want %+v", recorded.Rollback, evidence)
	}

	stored, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get after record: found=%t err=%v", found, err)
	}
	if stored.Stage != model.UpgradeStageRolledBack {
		t.Fatalf("persisted stage = %q", stored.Stage)
	}
	if stored.Rollback == nil || *stored.Rollback != evidence {
		t.Fatalf("persisted evidence = %+v, want %+v", stored.Rollback, evidence)
	}
	// Provenance survives the verdict: only the stage and the observation move.
	if stored.SourceKind != before.SourceKind || stored.SourceRoot != before.SourceRoot ||
		stored.SourceVersion != before.SourceVersion ||
		stored.SourceFingerprintBefore != before.SourceFingerprintBefore ||
		stored.SourceFingerprintAfter != before.SourceFingerprintAfter ||
		stored.BackupManifestSHA256 != before.BackupManifestSHA256 {
		t.Fatalf("rollback rewrote provenance: before=%+v after=%+v", before, stored)
	}
	if len(stored.DomainsDone) != len(before.DomainsDone) {
		t.Fatalf("rollback changed domains: before=%v after=%v", before.DomainsDone, stored.DomainsDone)
	}
	if stored.UpdatedAtMS < before.UpdatedAtMS {
		t.Fatalf("updated_at_ms moved backwards: %d -> %d", before.UpdatedAtMS, stored.UpdatedAtMS)
	}
}

// TestRecordRollbackRefusesASecondVerdict keeps the row's evidence the one that
// first proved the rollback.
func TestRecordRollbackRefusesASecondVerdict(t *testing.T) {
	ctx := context.Background()
	repository, _ := newRepository(t)
	if _, err := repository.RecordRollback(ctx, model.RollbackEvidence{}); err == nil {
		t.Fatal("a verdict without a recorded operation was written")
	}
	if _, found, err := repository.Get(ctx); err != nil || found {
		t.Fatalf("a refused verdict created a row: found=%t err=%v", found, err)
	}

	recordedCopyIn(t, repository)
	first := rollbackEvidence()
	if _, err := repository.RecordRollback(ctx, first); err != nil {
		t.Fatalf("record the first verdict: %v", err)
	}
	before, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("read the recorded verdict: %v", err)
	}
	second := first
	second.Image = "seakee/cpa-manager-plus:v1.14.8"
	if _, err := repository.RecordRollback(ctx, second); !errors.Is(err, upgradeoperation.ErrStageRegression) {
		t.Fatalf("second verdict = %v, want %v", err, upgradeoperation.ErrStageRegression)
	}
	after, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("re-read the recorded verdict: %v", err)
	}
	if after.UpdatedAtMS != before.UpdatedAtMS || after.Rollback == nil || *after.Rollback != first {
		t.Fatalf("a refused second verdict changed the row: %+v", after)
	}
}

// TestRecordRollbackRefusesIncompleteEvidence keeps a half-written observation
// out of the row.
func TestRecordRollbackRefusesIncompleteEvidence(t *testing.T) {
	ctx := context.Background()
	repository, _ := newRepository(t)
	recordedCopyIn(t, repository)
	complete := rollbackEvidence()
	for name, mutate := range map[string]func(*model.RollbackEvidence){
		"no image":       func(e *model.RollbackEvidence) { e.Image = "" },
		"no endpoint":    func(e *model.RollbackEvidence) { e.StatusURL = "" },
		"no fingerprint": func(e *model.RollbackEvidence) { e.SourceFingerprint = "" },
		"no observation": func(e *model.RollbackEvidence) { e.ObservedAtMS = 0 },
	} {
		evidence := complete
		mutate(&evidence)
		if _, err := repository.RecordRollback(ctx, evidence); err == nil {
			t.Fatalf("%s was recorded", name)
		}
		stored, _, err := repository.Get(ctx)
		if err != nil {
			t.Fatalf("%s: read the row: %v", name, err)
		}
		if stored.Stage != model.UpgradeStageCopyIn || stored.Rollback != nil {
			t.Fatalf("%s moved the row: %+v", name, stored)
		}
	}
}

// TestAdvanceRefusesTheTerminalVerdict covers L-D at the first stage-writing
// path: Advance carries no observation, so it can never write rolled_back. A
// terminal verdict nobody can check would otherwise be one call away.
func TestAdvanceRefusesTheTerminalVerdict(t *testing.T) {
	ctx := context.Background()
	repository, _ := newRepository(t)
	recordedCopyIn(t, repository)
	before, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("read the recorded copy-in: %v", err)
	}
	if _, err := repository.Advance(ctx, model.UpgradeDomainSourceCopy, model.UpgradeStageRolledBack); !errors.Is(err, upgradeoperation.ErrRollbackEvidenceRequired) {
		t.Fatalf("advance to rolled_back = %v, want %v", err, upgradeoperation.ErrRollbackEvidenceRequired)
	}
	after, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("read the row after the refusal: %v", err)
	}
	if after.Stage != before.Stage || after.Rollback != nil || after.UpdatedAtMS != before.UpdatedAtMS {
		t.Fatalf("a refused advance wrote the row: before=%+v after=%+v", before, after)
	}
}

// TestPutRefusesARolledBackRowWithoutEvidence covers L-D at the replacement
// path: a row that names the terminal stage but no observation never reaches the
// table, so a reader cannot find a verdict it cannot check.
func TestPutRefusesARolledBackRowWithoutEvidence(t *testing.T) {
	ctx := context.Background()
	repository, _ := newRepository(t)
	before := recordedCopyIn(t, repository)
	before.Stage = model.UpgradeStageRolledBack
	before.Rollback = nil
	if err := repository.Put(ctx, before); !errors.Is(err, upgradeoperation.ErrRollbackEvidenceRequired) {
		t.Fatalf("put a rolled_back row without evidence = %v, want %v", err, upgradeoperation.ErrRollbackEvidenceRequired)
	}
	stored, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("read the row after the refusal: found=%t err=%v", found, err)
	}
	if stored.Stage == model.UpgradeStageRolledBack {
		t.Fatalf("the evidence-less verdict was written: %+v", stored)
	}
}

// TestRecordSourceRootBackfillsOnce covers the repair an operation written by an
// earlier build needs: the directory is filled in when it is missing, and a row
// that already names one is never rewritten.
func TestRecordSourceRootBackfillsOnce(t *testing.T) {
	ctx := context.Background()
	repository, _ := newRepository(t)
	recorded := recordedCopyIn(t, repository)
	recorded.SourceRoot = ""
	if err := repository.Put(ctx, recorded); err != nil {
		t.Fatalf("clear the recorded source root: %v", err)
	}

	backfilled, err := repository.RecordSourceRoot(ctx, "/srv/cpamp/data")
	if err != nil {
		t.Fatalf("backfill the source root: %v", err)
	}
	if backfilled.SourceRoot != "/srv/cpamp/data" {
		t.Fatalf("backfilled root = %q", backfilled.SourceRoot)
	}
	stored, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("read the backfilled row: %v", err)
	}
	if stored.SourceRoot != "/srv/cpamp/data" {
		t.Fatalf("persisted root = %q", stored.SourceRoot)
	}
	if stored.SourceFingerprintBefore != recorded.SourceFingerprintBefore ||
		stored.BackupManifestSHA256 != recorded.BackupManifestSHA256 ||
		stored.SourceVersion != recorded.SourceVersion {
		t.Fatalf("the backfill rewrote provenance: %+v", stored)
	}

	again, err := repository.RecordSourceRoot(ctx, "/somewhere/else")
	if err != nil {
		t.Fatalf("re-record the source root: %v", err)
	}
	if again.SourceRoot != "/srv/cpamp/data" {
		t.Fatalf("a recorded root was rewritten to %q", again.SourceRoot)
	}
}

// TestRecordSourceRootNeedsARecordedOperation keeps the repair inside an
// existing operation: a backfill never creates a row.
func TestRecordSourceRootNeedsARecordedOperation(t *testing.T) {
	ctx := context.Background()
	repository, _ := newRepository(t)
	if _, err := repository.RecordSourceRoot(ctx, "/srv/cpamp/data"); err == nil {
		t.Fatal("a backfill created an operation row")
	}
	if _, found, err := repository.Get(ctx); err != nil || found {
		t.Fatalf("a refused backfill created a row: found=%t err=%v", found, err)
	}
}

// TestPutKeepsRecordedRollbackEvidence keeps Put a full replacement of the row:
// a resnapshotted row carries the evidence it read.
func TestPutKeepsRecordedRollbackEvidence(t *testing.T) {
	ctx := context.Background()
	repository, _ := newRepository(t)
	recordedCopyIn(t, repository)
	evidence := rollbackEvidence()
	if _, err := repository.RecordRollback(ctx, evidence); err != nil {
		t.Fatalf("record rollback: %v", err)
	}
	stored, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := repository.Put(ctx, stored); err != nil {
		t.Fatalf("put the read row back: %v", err)
	}
	again, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("re-read the row: %v", err)
	}
	if again.Rollback == nil || *again.Rollback != evidence {
		t.Fatalf("re-written row lost its evidence: %+v", again.Rollback)
	}
}

// TestGetReportsATerminalRowWithoutEvidence covers the read half of the write
// invariant: a store written by a build that recorded the verdict without its
// observation holds a row this build refuses to write on both paths. The reader
// names that case, so a read-only caller can report the store rather than a
// malformed row.
func TestGetReportsATerminalRowWithoutEvidence(t *testing.T) {
	for name, statement := range map[string]string{
		"no evidence": `update upgrade_operations
			set stage = 'rolled_back',
				rollback_image = null, rollback_status_url = null,
				rollback_source_fingerprint = null, rollback_observed_at_ms = null
			where id = 1`,
		"partial evidence": `update upgrade_operations
			set stage = 'rolled_back',
				rollback_image = 'seakee/cpa-manager-plus:v1.14.7',
				rollback_status_url = null, rollback_source_fingerprint = null,
				rollback_observed_at_ms = null
			where id = 1`,
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repository, db := newRepository(t)
			recordedCopyIn(t, repository)
			// Write behind the repository: this is the row the production paths
			// refuse, so only a build from before the invariant could leave it.
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("write the evidence-less verdict: %v", err)
			}
			_, found, err := repository.Get(ctx)
			if !errors.Is(err, upgradeoperation.ErrRollbackEvidenceMissing) {
				t.Fatalf("get = %v (found=%t), want %v", err, found, upgradeoperation.ErrRollbackEvidenceMissing)
			}
			if errors.Is(err, upgradeoperation.ErrInvalidRow) {
				t.Fatalf("an outdated store was reported as a malformed row: %v", err)
			}
		})
	}
}

// TestRecordRollbackKeepsAFutureDatedRowReadable is the regression for M4 at the
// verdict writer: updated_at_ms was stamped from the wall clock, so a row whose
// clock ran ahead (or was restored from a later backup) was written with
// updated_at_ms below created_at_ms and every later reader rejected it.
func TestRecordRollbackKeepsAFutureDatedRowReadable(t *testing.T) {
	repository, db := newRepository(t)
	ctx := context.Background()
	future := time.Now().Add(time.Hour).UnixMilli()
	if _, err := db.ExecContext(ctx, `insert into upgrade_operations
		(id, stage, domains_done, reconciliation, manual_items, source_kind, source_version,
		 source_root, source_fingerprint_before, source_fingerprint_after, backup_manifest_sha256,
		 created_at_ms, updated_at_ms)
		values (1, ?, '[]', '{}', '[]', 'upgraded-legacy', 'v1.14.7', '/srv/cpamp/data',
		 'before', 'after', 'manifest', ?, ?)`,
		string(model.UpgradeStageCopyIn), future, future); err != nil {
		t.Fatalf("seed a future-dated row: %v", err)
	}
	recorded, err := repository.RecordRollback(ctx, rollbackEvidence())
	if err != nil {
		t.Fatalf("record a verdict on a future-dated row: %v", err)
	}
	if recorded.UpdatedAtMS < future {
		t.Fatalf("updated_at_ms = %d moved before the stored %d", recorded.UpdatedAtMS, future)
	}
	stored, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("read the verdict back: found=%t err=%v", found, err)
	}
	if stored.Stage != model.UpgradeStageRolledBack || stored.Rollback == nil {
		t.Fatalf("stored row = %+v, want the verdict with evidence", stored)
	}
	if stored.UpdatedAtMS < stored.CreatedAtMS {
		t.Fatalf("updated_at_ms = %d is before created_at_ms = %d", stored.UpdatedAtMS, stored.CreatedAtMS)
	}
}

// TestRecordSourceRootKeepsAFutureDatedRowReadable is the same invariant at the
// backfill writer.
func TestRecordSourceRootKeepsAFutureDatedRowReadable(t *testing.T) {
	repository, db := newRepository(t)
	ctx := context.Background()
	future := time.Now().Add(time.Hour).UnixMilli()
	if _, err := db.ExecContext(ctx, `insert into upgrade_operations
		(id, stage, domains_done, reconciliation, manual_items, created_at_ms, updated_at_ms)
		values (1, ?, '[]', '{}', '[]', ?, ?)`,
		string(model.UpgradeStageCopyIn), future, future); err != nil {
		t.Fatalf("seed a future-dated row: %v", err)
	}
	backfilled, err := repository.RecordSourceRoot(ctx, "/srv/cpamp/data")
	if err != nil {
		t.Fatalf("backfill a future-dated row: %v", err)
	}
	if backfilled.UpdatedAtMS < future {
		t.Fatalf("updated_at_ms = %d moved before the stored %d", backfilled.UpdatedAtMS, future)
	}
	stored, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("read the backfilled row: found=%t err=%v", found, err)
	}
	if stored.SourceRoot != "/srv/cpamp/data" || stored.UpdatedAtMS < stored.CreatedAtMS {
		t.Fatalf("stored row = %+v", stored)
	}
}
