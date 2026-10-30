package upgradeoperation_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

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
