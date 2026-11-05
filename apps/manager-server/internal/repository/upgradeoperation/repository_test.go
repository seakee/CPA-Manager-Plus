package upgradeoperation_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"strings"
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
		ImageSource:       "operator",
		ImageForm:         "release-tag",
		StatusURL:         "http://127.0.0.1:18317/health",
		SourceFingerprint: "observed-after-the-v1-restart",
		ObservedAtMS:      1_700_000_000_000,
	}
}

// recordedCopyIn leaves the row the copy-in would have recorded. The row is
// re-read after the write, so the revision it carries is the one on disk: a
// caller that replaced this row again would otherwise hold a token Put has
// already spent.
func recordedCopyIn(t *testing.T, repository *upgradeoperation.Repository) model.UpgradeOperation {
	t.Helper()
	ctx := context.Background()
	recorded, err := repository.Advance(ctx, model.UpgradeDomainSourceCopy, model.UpgradeStageCopyIn)
	if err != nil {
		t.Fatalf("advance to copy_in: %v", err)
	}
	recorded.SourceKind = "upgraded-legacy"
	recorded.SourceVersion = "v1.14.7"
	recorded.SourceRoot = "/srv/cpamp/data"
	recorded.SourceFingerprintBefore = "before"
	recorded.SourceFingerprintAfter = "after"
	recorded.BackupManifestSHA256 = "manifest"
	if err := repository.Put(ctx, recorded); err != nil {
		t.Fatalf("record provenance: %v", err)
	}
	stored, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("read the recorded copy-in row: found=%t err=%v", found, err)
	}
	return stored
}

// completionEvidence is a whole verification observation: every field the
// complete verdict rests on. Nothing here is a stage claim — a test that stores
// `complete` stores this beside it, exactly as the gate does.
func completionEvidence(domains ...string) model.UpgradeVerification {
	if len(domains) == 0 {
		domains = []string{model.UpgradeDomainSourceCopy}
	}
	return model.UpgradeVerification{
		TargetMode:          model.UpgradeTargetExternal,
		SourceFingerprint:   "sha256:source-manifest",
		Domains:             domains,
		BindingIdentity:     "https://cpa.example",
		BindingGeneration:   "https://cpa.example",
		BindingObservedAtMS: 1_700_000_000_000,
		ObservedAtMS:        1_700_000_000_001,
	}
}

// recordCompletion drives the one production path into the complete stage: the
// gate's evidence and the verdict in a single transaction. A test that needs a
// completed row asks for one here instead of writing the stage itself, which the
// repository refuses.
func recordCompletion(t *testing.T, db *sql.DB, verification model.UpgradeVerification) model.UpgradeOperation {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin the completion transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	operation, err := upgradeoperation.RecordCompletionTx(ctx, tx, verification)
	if err != nil {
		t.Fatalf("record the completion verdict: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit the completion verdict: %v", err)
	}
	return operation
}

// TestAdvanceAfterAClockRollbackKeepsTheRowReadable pins the repository-owned
// updated_at_ms. A row whose recorded time lies ahead of the wall clock — the host
// clock moved back, or the store was restored from a backup written later — used
// to be advanced with the current clock, which produced a row Get rejects with
// ErrInvalidRow: status, the next advance and every later reader were blocked
// until the clock caught up.
func TestAdvanceAfterAClockRollbackKeepsTheRowReadable(t *testing.T) {
	repository, db := newRepository(t)
	recordedCopyIn(t, repository)
	future := time.Now().Add(time.Hour).UnixMilli()
	if _, err := db.Exec(`update upgrade_operations set created_at_ms = ?, updated_at_ms = ? where id = 1`, future, future); err != nil {
		t.Fatalf("seed a future-dated row: %v", err)
	}
	advanced, err := repository.Advance(context.Background(), model.UpgradeDomainUsageEvents, model.UpgradeStageReconcile)
	if err != nil {
		t.Fatalf("advance a row whose clock ran ahead: %v", err)
	}
	if advanced.UpdatedAtMS < future {
		t.Fatalf("advanced updated_at_ms = %d, want at least the stored %d", advanced.UpdatedAtMS, future)
	}
	loaded, found, err := repository.Get(context.Background())
	if err != nil || !found {
		t.Fatalf("the advanced row is unreadable (found %t): %v", found, err)
	}
	if loaded.UpdatedAtMS < future {
		t.Fatalf("stored updated_at_ms = %d, want at least the seeded %d", loaded.UpdatedAtMS, future)
	}
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
	repository, db := newRepository(t)
	ctx := context.Background()
	if _, err := repository.Advance(ctx, model.UpgradeDomainSourceCopy, model.UpgradeStageVerify); err != nil {
		t.Fatalf("advance verify: %v", err)
	}
	recordCompletion(t, db, completionEvidence(model.UpgradeDomainSourceCopy))
	before, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get completed row: %v", err)
	}
	for _, stage := range []model.UpgradeStage{model.UpgradeStageVerify, model.UpgradeStageFailed} {
		if _, err := repository.Advance(ctx, model.UpgradeDomainCoverage, stage); !errors.Is(err, upgradeoperation.ErrStageRegression) {
			t.Fatalf("advance complete -> %q error = %v, want ErrStageRegression", stage, err)
		}
	}
	// Naming the verdict again is the stage-level refusal, not a stage move: a
	// generic advance carries no verification, so it could only claim the stage.
	if _, err := repository.Advance(ctx, model.UpgradeDomainCoverage, model.UpgradeStageComplete); !errors.Is(err, upgradeoperation.ErrCompleteRequiresGate) {
		t.Fatalf("advance into complete error = %v, want ErrCompleteRequiresGate", err)
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
	repository, db := newRepository(t)
	ctx := context.Background()
	if _, err := repository.Advance(ctx, model.UpgradeDomainSourceCopy, model.UpgradeStageVerify); err != nil {
		t.Fatalf("advance verify: %v", err)
	}
	recordCompletion(t, db, completionEvidence(model.UpgradeDomainSourceCopy))
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
	// A whole-row proposal that names the verdict is refused before the row is
	// read: Put carries no verification, so it could only claim the stage.
	if err := repository.Put(ctx, complete); !errors.Is(err, upgradeoperation.ErrCompleteRequiresGate) {
		t.Fatalf("put complete error = %v, want ErrCompleteRequiresGate", err)
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

// TestAdvanceCannotRewindACompletedRow is the R10 regression for Advance, kept
// under the gate rule: complete is no longer reachable from Advance at all, and a
// stored completed row is frozen. Appending a domain, parking the row, or naming
// the verdict again leaves the row exactly as the gate recorded it.
func TestAdvanceCannotRewindACompletedRow(t *testing.T) {
	repository, db := newRepository(t)
	ctx := context.Background()
	if _, err := repository.Advance(ctx, model.UpgradeDomainUsageEvents, model.UpgradeStageVerify); err != nil {
		t.Fatalf("advance verify: %v", err)
	}
	complete := recordCompletion(t, db, completionEvidence(model.UpgradeDomainUsageEvents))
	if complete.Stage != model.UpgradeStageComplete {
		t.Fatalf("stage = %q, want complete", complete.Stage)
	}
	if _, err := repository.Advance(ctx, model.UpgradeDomainCoverage, model.UpgradeStageComplete); !errors.Is(err, upgradeoperation.ErrCompleteRequiresGate) {
		t.Fatalf("advance complete with a new domain error = %v, want ErrCompleteRequiresGate", err)
	}
	assertStoredOperation(t, repository, complete)

	if _, err := repository.Advance(ctx, "", model.UpgradeStageFailed); !errors.Is(err, upgradeoperation.ErrStageRegression) {
		t.Fatalf("advance complete -> failed error = %v, want ErrStageRegression", err)
	}
	assertStoredOperation(t, repository, complete)

	// The repeat is refused too: the verdict and its evidence are one record
	// written by the gate, and a generic advance has no evidence to repeat.
	if _, err := repository.Advance(ctx, model.UpgradeDomainUsageEvents, model.UpgradeStageComplete); !errors.Is(err, upgradeoperation.ErrCompleteRequiresGate) {
		t.Fatalf("repeated advance into complete error = %v, want ErrCompleteRequiresGate", err)
	}
	assertStoredOperation(t, repository, complete)
}

// TestGenericWritersRefuseCompleteBeforeTheRowExists closes the creation side:
// there is no write, first or repeated, that creates an operation in complete.
// The stage is only sound with the verification the gate records beside it, so
// both generic writers refuse it before the row is read and nothing is created.
func TestGenericWritersRefuseCompleteBeforeTheRowExists(t *testing.T) {
	repository, db := newRepository(t)
	ctx := context.Background()
	if _, err := repository.Advance(ctx, "", model.UpgradeStageComplete); !errors.Is(err, upgradeoperation.ErrCompleteRequiresGate) {
		t.Fatalf("first advance into complete error = %v, want ErrCompleteRequiresGate", err)
	}
	if err := repository.Put(ctx, model.UpgradeOperation{
		Stage:                model.UpgradeStageComplete,
		DomainsDone:          []string{model.UpgradeDomainCoverage},
		BackupManifestSHA256: "sha256:first",
		Reconciliation:       json.RawMessage(`{"checked":true}`),
		ManualItems:          []string{"hand over the copied database"},
	}); !errors.Is(err, upgradeoperation.ErrCompleteRequiresGate) {
		t.Fatalf("first put into complete error = %v, want ErrCompleteRequiresGate", err)
	}
	if _, found, err := repository.Get(ctx); err != nil || found {
		t.Fatalf("a refused completion left a row behind: found=%t err=%v", found, err)
	}
	// The gate itself continues an operation, it never creates one: a store with
	// no recorded operation has nothing to complete.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin the completion transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := upgradeoperation.RecordCompletionTx(ctx, tx, completionEvidence()); !errors.Is(err, upgradeoperation.ErrInvalidRow) {
		t.Fatalf("completion on an empty store error = %v, want ErrInvalidRow", err)
	}
}

// TestPutCannotChangeAStoredCompleteRow is the R10 regression for Put: a
// same-stage Put replaced observations on a completed row because the stage
// comparison accepted complete -> complete. The verdict is frozen now, and the
// stage itself is refused before the row is even read.
func TestPutCannotChangeAStoredCompleteRow(t *testing.T) {
	repository, db := newRepository(t)
	ctx := context.Background()
	if _, err := repository.Advance(ctx, model.UpgradeDomainSourceCopy, model.UpgradeStageVerify); err != nil {
		t.Fatalf("advance verify: %v", err)
	}
	recordCompletion(t, db, completionEvidence(model.UpgradeDomainSourceCopy))
	complete, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get completed row: found=%t err=%v", found, err)
	}
	changedManifest := complete
	changedManifest.Stage = model.UpgradeStageVerify
	changedManifest.BackupManifestSHA256 = "sha256:later"
	changedManifest.ManualItems = []string{"confirm the archive copy"}
	if err := repository.Put(ctx, changedManifest); !errors.Is(err, upgradeoperation.ErrStageRegression) {
		t.Fatalf("put changing a completed row error = %v, want ErrStageRegression", err)
	}
	assertStoredOperation(t, repository, complete)

	changedDomains := changedManifest
	changedDomains.DomainsDone = []string{model.UpgradeDomainCoverage}
	if err := repository.Put(ctx, changedDomains); !errors.Is(err, upgradeoperation.ErrStageRegression) {
		t.Fatalf("put adding a domain to a completed row error = %v, want ErrStageRegression", err)
	}
	assertStoredOperation(t, repository, complete)

	// A proposal that keeps the verdict's stage is refused by the stage rule
	// before the row is read: Put carries no verification, so it could only
	// claim the stage again.
	if err := repository.Put(ctx, complete); !errors.Is(err, upgradeoperation.ErrCompleteRequiresGate) {
		t.Fatalf("identical put on a completed row error = %v, want ErrCompleteRequiresGate", err)
	}
	assertStoredOperation(t, repository, complete)
}

// emptyVerification reports the schema default and the empty value, neither of
// which is a verdict the gate wrote.
func emptyVerification(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed == "" || trimmed == "{}"
}

// TestPutCarriesTheRecordedVerificationForward keeps the evidence the gate wrote
// out of every generic writer's reach. A whole-row Put replaces every column, so
// a caller that proposed its own verification block would otherwise leave the row
// carrying a record no reader could re-check.
func TestPutCarriesTheRecordedVerificationForward(t *testing.T) {
	repository, _ := newRepository(t)
	ctx := context.Background()
	if _, err := repository.Advance(ctx, model.UpgradeDomainSourceCopy, model.UpgradeStageVerify); err != nil {
		t.Fatalf("advance verify: %v", err)
	}
	recorded, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get the verify row: found=%t err=%v", found, err)
	}
	if recorded.Stage != model.UpgradeStageVerify {
		t.Fatalf("stage = %q, want verify", recorded.Stage)
	}
	if !emptyVerification(recorded.Verification) {
		t.Fatalf("a generic advance recorded verification: %s", recorded.Verification)
	}

	proposal := recorded
	proposal.Verification = json.RawMessage(`{"targetMode":"managed"}`)
	proposal.ManualItems = append(append([]string{}, recorded.ManualItems...), "manual: a second instruction")
	if err := repository.Put(ctx, proposal); err != nil {
		t.Fatalf("put on the verify row: %v", err)
	}
	stored, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get the verify row: found=%t err=%v", found, err)
	}
	if !emptyVerification(stored.Verification) {
		t.Fatalf("generic write fabricated a verification the gate never wrote: %s", stored.Verification)
	}
	if len(stored.ManualItems) != 1 {
		t.Fatalf("manual items = %v, want the one new instruction", stored.ManualItems)
	}
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
		'|' || quote(verification) ||
		'|' || quote(manual_items) || '|' || quote(error_code) || '|' || quote(error_detail) ||
		'|' || quote(created_at_ms) || '|' || quote(updated_at_ms)
		from upgrade_operations where id = 1`).Scan(&row); err != nil {
		t.Fatalf("read raw row: %v", err)
	}
	return row
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

// TestRecordRollbackRefusesAStageWithNoCopiedRoot is the L10 regression: stage
// eligibility is decided again inside the write transaction, not only by the
// service precheck. A row at preflight never copied a root, and a failed row has
// no position, so it may have failed before the copy-in; neither position can
// justify a verdict that says the v2 root was abandoned. The stage is changed
// here behind the service's back, which is exactly the staleness the
// transaction has to catch.
func TestRecordRollbackRefusesAStageWithNoCopiedRoot(t *testing.T) {
	for _, stage := range []model.UpgradeStage{model.UpgradeStagePreflight, model.UpgradeStageFailed} {
		t.Run(string(stage), func(t *testing.T) {
			ctx := context.Background()
			repository, _ := newRepository(t)
			if _, err := repository.Advance(ctx, "", stage); err != nil {
				t.Fatalf("record the %s stage: %v", stage, err)
			}
			before, found, err := repository.Get(ctx)
			if err != nil || !found {
				t.Fatalf("read the recorded stage: found=%t err=%v", found, err)
			}
			if _, err := repository.RecordRollback(ctx, rollbackEvidence()); !errors.Is(err, upgradeoperation.ErrStageRegression) {
				t.Fatalf("rollback from %q error = %v, want %v", stage, err, upgradeoperation.ErrStageRegression)
			}
			after, found, err := repository.Get(ctx)
			if err != nil || !found {
				t.Fatalf("re-read the refused row: found=%t err=%v", found, err)
			}
			if after.Stage != before.Stage || after.UpdatedAtMS != before.UpdatedAtMS || after.Rollback != nil {
				t.Fatalf("a refused verdict changed the %q row: before=%+v after=%+v", stage, before, after)
			}
		})
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
		"no image":        func(e *model.RollbackEvidence) { e.Image = "" },
		"no image source": func(e *model.RollbackEvidence) { e.ImageSource = "" },
		"no image form":   func(e *model.RollbackEvidence) { e.ImageForm = "" },
		"no endpoint":     func(e *model.RollbackEvidence) { e.StatusURL = "" },
		"no fingerprint":  func(e *model.RollbackEvidence) { e.SourceFingerprint = "" },
		"no observation":  func(e *model.RollbackEvidence) { e.ObservedAtMS = 0 },
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
		(id, stage, last_forward_stage, domains_done, reconciliation, manual_items, source_kind, source_version,
		 source_root, source_fingerprint_before, source_fingerprint_after, backup_manifest_sha256,
		 created_at_ms, updated_at_ms)
		values (1, ?, ?, '[]', '{}', '[]', 'upgraded-legacy', 'v1.14.7', '/srv/cpamp/data',
		 'before', 'after', 'manifest', ?, ?)`,
		string(model.UpgradeStageCopyIn), string(model.UpgradeStageCopyIn), future, future); err != nil {
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
		(id, stage, last_forward_stage, domains_done, reconciliation, manual_items, created_at_ms, updated_at_ms)
		values (1, ?, ?, '[]', '{}', '[]', ?, ?)`,
		string(model.UpgradeStageCopyIn), string(model.UpgradeStageCopyIn), future, future); err != nil {
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

// TestEveryWriterKeepsUpdatedAtMonotonic pins the shared invariant: the
// repository owns updated_at_ms, so no writer may stamp a clock that is behind
// the stored row.
//
// The fixture moves the stamp two hours ahead, which is what a backwards clock
// looks like from here: a host whose clock ran fast, a row restored from a backup
// written later, or a process that started before the stored value was recorded.
// Every writer that touches the row — including the refusal and clear paths the
// External step added — must keep the stamp at least where it was instead of
// overwriting it with its own now.
func TestEveryWriterKeepsUpdatedAtMonotonic(t *testing.T) {
	ctx := context.Background()
	repository, db := newRepository(t)
	recordedCopyIn(t, repository)
	future := time.Now().Add(2 * time.Hour).UnixMilli()

	storedStamp := func() int64 {
		var stamp int64
		if err := db.QueryRowContext(ctx,
			`select updated_at_ms from upgrade_operations where id = 1`).Scan(&stamp); err != nil {
			t.Fatalf("read the stamp: %v", err)
		}
		return stamp
	}
	// moveAhead rewrites the row the way a clock that ran fast would have, and
	// keeps it valid: created_at_ms stays before the stamp it owns.
	moveAhead := func() {
		if _, err := db.ExecContext(ctx,
			`update upgrade_operations set created_at_ms = ?, updated_at_ms = ? where id = 1`,
			future-1000, future); err != nil {
			t.Fatalf("move the stamp ahead: %v", err)
		}
	}
	inTransaction := func(run func(tx *sql.Tx) error) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := run(tx); err != nil {
			_ = tx.Rollback()
			t.Fatalf("write: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	writers := []struct {
		name string
		run  func()
	}{
		{name: "RecordError", run: func() {
			if err := repository.RecordError(ctx, "external_target_moved", "stale refusal"); err != nil {
				t.Fatalf("record the refusal: %v", err)
			}
		}},
		{name: "ClearErrorTx", run: func() {
			inTransaction(func(tx *sql.Tx) error {
				return upgradeoperation.ClearErrorTx(ctx, tx)
			})
		}},
		{name: "Advance", run: func() {
			if _, err := repository.Advance(ctx, model.UpgradeDomainUsageEvents, model.UpgradeStageReconcile); err != nil {
				t.Fatalf("advance: %v", err)
			}
		}},
		{name: "RecordReconciliationTx", run: func() {
			inTransaction(func(tx *sql.Tx) error {
				return upgradeoperation.RecordReconciliationTx(ctx, tx, json.RawMessage(`{"days":{}}`))
			})
		}},
		{name: "Put", run: func() {
			stored, found, err := repository.Get(ctx)
			if err != nil || !found {
				t.Fatalf("read the row: found=%t err=%v", found, err)
			}
			// A caller proposal older than the stored stamp, but still after
			// created_at_ms: the repository must not pin it onto the row.
			stored.UpdatedAtMS = future - 500
			if err := repository.Put(ctx, stored); err != nil {
				t.Fatalf("put: %v", err)
			}
		}},
		{name: "RecordRollback", run: func() {
			if _, err := repository.RecordRollback(ctx, rollbackEvidence()); err != nil {
				t.Fatalf("record the rollback: %v", err)
			}
		}},
	}
	for _, writer := range writers {
		moveAhead()
		writer.run()
		// Every writer is reported independently: the invariant is per writer, and
		// one that rewinds must not hide the others.
		if got := storedStamp(); got < future {
			t.Errorf("%s moved updated_at_ms backwards: %d < %d", writer.name, got, future)
		}
		if _, _, err := repository.Get(ctx); err != nil {
			t.Errorf("%s left a row Get rejects: %v", writer.name, err)
		}
	}
}

// TestRecordErrorNeedsARecordedOperation keeps a refusal inside an existing
// operation: recording one never creates a row.
func TestRecordErrorNeedsARecordedOperation(t *testing.T) {
	ctx := context.Background()
	repository, _ := newRepository(t)
	if err := repository.RecordError(ctx, "external_target_moved", "stale refusal"); !errors.Is(err, upgradeoperation.ErrInvalidRow) {
		t.Fatalf("err = %v, want ErrInvalidRow", err)
	}
	if _, found, err := repository.Get(ctx); err != nil || found {
		t.Fatalf("a refused refusal created a row: found=%t err=%v", found, err)
	}
}

// TestStaleRevisionWriteIsRejectedAfterAnIntegratedWriter is the regression for
// merging the two U1 halves. The writers the D harness added — the External
// step's refusal and clear paths, the reconciliation record, the source-root
// backfill and the rollback verdict — updated the row without consulting its
// revision, so a caller holding the revision it read before them could still
// write with Put and silently overwrite what they recorded. Every one of them
// now stores revision+1 behind the revision it read, so that write is refused
// with ErrConflict and the row keeps the writer's work. Advance is the control:
// it already carried the revision, and it still does.
func TestStaleRevisionWriteIsRejectedAfterAnIntegratedWriter(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		seed  func(t *testing.T, repository *upgradeoperation.Repository)
		apply func(t *testing.T, repository *upgradeoperation.Repository, db *sql.DB)
	}{
		{
			name: "record error",
			seed: func(t *testing.T, repository *upgradeoperation.Repository) {
				_ = recordedCopyIn(t, repository)
			},
			apply: func(t *testing.T, repository *upgradeoperation.Repository, _ *sql.DB) {
				if err := repository.RecordError(ctx, "upgrade_external_connect_refused", "the target refused the credential"); err != nil {
					t.Fatalf("record the refusal: %v", err)
				}
			},
		},
		{
			name: "clear error",
			seed: func(t *testing.T, repository *upgradeoperation.Repository) {
				_ = recordedCopyIn(t, repository)
				if err := repository.RecordError(ctx, "upgrade_external_connect_refused", "the target refused the credential"); err != nil {
					t.Fatalf("record the refusal: %v", err)
				}
			},
			apply: func(t *testing.T, repository *upgradeoperation.Repository, db *sql.DB) {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatalf("begin: %v", err)
				}
				if err := upgradeoperation.ClearErrorTx(ctx, tx); err != nil {
					_ = tx.Rollback()
					t.Fatalf("clear the refusal: %v", err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatalf("commit: %v", err)
				}
			},
		},
		{
			name: "append manual items",
			seed: func(t *testing.T, repository *upgradeoperation.Repository) {
				_ = recordedCopyIn(t, repository)
			},
			apply: func(t *testing.T, repository *upgradeoperation.Repository, db *sql.DB) {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatalf("begin: %v", err)
				}
				if _, err := upgradeoperation.AppendManualItemsTx(ctx, tx, []string{"the Managed CPA import deferred every item"}); err != nil {
					_ = tx.Rollback()
					t.Fatalf("append the manual items: %v", err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatalf("commit: %v", err)
				}
			},
		},
		{
			name: "record reconciliation",
			seed: func(t *testing.T, repository *upgradeoperation.Repository) {
				_ = recordedCopyIn(t, repository)
			},
			apply: func(t *testing.T, repository *upgradeoperation.Repository, _ *sql.DB) {
				if err := repository.RecordReconciliation(ctx, json.RawMessage(`{"usageDays":1}`)); err != nil {
					t.Fatalf("record the reconciliation: %v", err)
				}
			},
		},
		{
			name: "backfill source root",
			seed: func(t *testing.T, repository *upgradeoperation.Repository) {
				if _, err := repository.Advance(ctx, model.UpgradeDomainSourceCopy, model.UpgradeStageCopyIn); err != nil {
					t.Fatalf("advance to copy_in: %v", err)
				}
			},
			apply: func(t *testing.T, repository *upgradeoperation.Repository, _ *sql.DB) {
				if _, err := repository.RecordSourceRoot(ctx, "/srv/cpamp/data"); err != nil {
					t.Fatalf("record the source root: %v", err)
				}
			},
		},
		{
			name: "record rollback",
			seed: func(t *testing.T, repository *upgradeoperation.Repository) {
				_ = recordedCopyIn(t, repository)
			},
			apply: func(t *testing.T, repository *upgradeoperation.Repository, _ *sql.DB) {
				if _, err := repository.RecordRollback(ctx, rollbackEvidence()); err != nil {
					t.Fatalf("record the verdict: %v", err)
				}
			},
		},
		{
			name: "advance",
			seed: func(t *testing.T, repository *upgradeoperation.Repository) {
				_ = recordedCopyIn(t, repository)
			},
			apply: func(t *testing.T, repository *upgradeoperation.Repository, _ *sql.DB) {
				if _, err := repository.Advance(ctx, model.UpgradeDomainPricing, model.UpgradeStageReconcile); err != nil {
					t.Fatalf("advance to reconcile: %v", err)
				}
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			repository, db := newRepository(t)
			testCase.seed(t, repository)
			stale, found, err := repository.Get(ctx)
			if err != nil || !found {
				t.Fatalf("read the seeded row: found=%t err=%v", found, err)
			}
			testCase.apply(t, repository, db)
			advanced, found, err := repository.Get(ctx)
			if err != nil || !found {
				t.Fatalf("read the written row: found=%t err=%v", found, err)
			}
			if advanced.Revision != stale.Revision+1 {
				t.Fatalf("stored revision %d, want %d: the writer did not advance the token a reader holds",
					advanced.Revision, stale.Revision+1)
			}
			if err := repository.Put(ctx, stale); !errors.Is(err, upgradeoperation.ErrConflict) {
				t.Fatalf("write with the revision read before the writer = %v, want %v", err, upgradeoperation.ErrConflict)
			}
			after, found, err := repository.Get(ctx)
			if err != nil || !found {
				t.Fatalf("read the row after the refused write: found=%t err=%v", found, err)
			}
			if !reflect.DeepEqual(after, advanced) {
				t.Fatalf("the refused write changed the row:\nbefore %+v\nafter  %+v", advanced, after)
			}
		})
	}
}

// TestCompletedOperationIsFrozenForEveryColumnWriter is the R4 regression: the
// completion gate froze the verdict, but the column writers could still replace
// the reconciliation, the refusal, the manual items, the source root or the
// External binding of a completed row through the revision CAS. A completed
// verdict is terminal, so every one of them refuses it and leaves the row and the
// bound target byte-for-byte unchanged.
func TestCompletedOperationIsFrozenForEveryColumnWriter(t *testing.T) {
	ctx := context.Background()
	repository, db := newRepository(t)
	recordedCopyIn(t, repository)
	if _, err := repository.Advance(ctx, model.UpgradeDomainCoverage, model.UpgradeStageVerify); err != nil {
		t.Fatalf("advance to verify: %v", err)
	}
	seedTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin the binding transaction: %v", err)
	}
	if err := upgradeoperation.RecordExternalBindingTx(ctx, seedTx, upgradeoperation.ExternalBinding{
		BaseURL:      "https://cpa.example",
		Identity:     "https://cpa.example",
		Generation:   "https://cpa.example",
		ObservedAtMS: 1_700_000_000_000,
	}); err != nil {
		_ = seedTx.Rollback()
		t.Fatalf("record the fixture binding: %v", err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatalf("commit the fixture binding: %v", err)
	}
	recordCompletion(t, db, completionEvidence(model.UpgradeDomainSourceCopy, model.UpgradeDomainCoverage))

	readBinding := func() string {
		t.Helper()
		var row string
		if err := db.QueryRow(`select quote(base_url) || '|' || quote(identity) || '|' || quote(generation) ||
			'|' || quote(server_fingerprint) || '|' || quote(cpa_version) || '|' || quote(support) ||
			'|' || quote(client_keys) || '|' || quote(observed_at_ms)
			from upgrade_external_connect where id = 1`).Scan(&row); err != nil {
			t.Fatalf("read the raw binding: %v", err)
		}
		return row
	}
	rowBefore := readRawOperation(t, db)
	bindingBefore := readBinding()

	transaction := func(t *testing.T) *sql.Tx {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin the writer transaction: %v", err)
		}
		t.Cleanup(func() { _ = tx.Rollback() })
		return tx
	}
	for _, test := range []struct {
		name string
		call func(t *testing.T) error
	}{
		{name: "RecordReconciliationTx", call: func(t *testing.T) error {
			return upgradeoperation.RecordReconciliationTx(ctx, transaction(t), json.RawMessage(`{"replaced":true}`))
		}},
		{name: "RecordReconciliation", call: func(t *testing.T) error {
			return repository.RecordReconciliation(ctx, json.RawMessage(`{"replaced":true}`))
		}},
		{name: "RecordError", call: func(t *testing.T) error {
			return repository.RecordError(ctx, "upgrade_reconcile_blocked", "a refusal after the verdict")
		}},
		{name: "RecordErrorTx", call: func(t *testing.T) error {
			_, err := upgradeoperation.RecordErrorTx(ctx, transaction(t), "upgrade_reconcile_blocked", "a refusal after the verdict")
			return err
		}},
		{name: "ClearErrorTx", call: func(t *testing.T) error {
			return upgradeoperation.ClearErrorTx(ctx, transaction(t))
		}},
		{name: "AppendManualItemsTx", call: func(t *testing.T) error {
			_, err := upgradeoperation.AppendManualItemsTx(ctx, transaction(t), []string{"manual: after the verdict"})
			return err
		}},
		{name: "RecordSourceRoot", call: func(t *testing.T) error {
			_, err := repository.RecordSourceRoot(ctx, "/srv/other-data")
			return err
		}},
		{name: "RecordExternalBindingTx", call: func(t *testing.T) error {
			return upgradeoperation.RecordExternalBindingTx(ctx, transaction(t), upgradeoperation.ExternalBinding{
				BaseURL:      "https://other-cpa.example",
				Identity:     "https://other-cpa.example",
				Generation:   "https://other-cpa.example",
				ObservedAtMS: 1_700_000_000_999,
			})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(t); !errors.Is(err, upgradeoperation.ErrStageRegression) {
				t.Fatalf("%s on a completed row error = %v, want ErrStageRegression", test.name, err)
			}
			if after := readRawOperation(t, db); after != rowBefore {
				t.Fatalf("%s changed the completed row:\n before %s\n after  %s", test.name, rowBefore, after)
			}
			if after := readBinding(); after != bindingBefore {
				t.Fatalf("%s changed the bound target:\n before %s\n after  %s", test.name, bindingBefore, after)
			}
		})
	}
}
