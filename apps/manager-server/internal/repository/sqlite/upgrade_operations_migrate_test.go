package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
)

// legacyUpgradeOperationDDL is the upgrade_operations schema before the
// compare-and-set revision and the forward high-water mark were added.
func legacyUpgradeOperationDDL() string {
	return `create table upgrade_operations (
		id integer primary key check(id = 1),
		stage text not null default '',
		domains_done text not null default '[]',
		source_kind text,
		source_version text,
		source_fingerprint_before text,
		source_fingerprint_after text,
		backup_manifest_sha256 text,
		reconciliation text not null default '{}',
		manual_items text not null default '[]',
		error_code text,
		error_detail text,
		created_at_ms integer not null,
		updated_at_ms integer not null
	)`
}

func seedLegacyUpgradeOperation(t *testing.T, path, stage, domains string, created, updated int64) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy fixture: %v", err)
	}
	if _, err := db.Exec(legacyUpgradeOperationDDL()); err != nil {
		_ = db.Close()
		t.Fatalf("seed legacy schema: %v", err)
	}
	if _, err := db.Exec(`insert into upgrade_operations (
			id, stage, domains_done, reconciliation, manual_items, created_at_ms, updated_at_ms
		) values (1, ?, ?, '{}', '[]', ?, ?)`, stage, domains, created, updated); err != nil {
		_ = db.Close()
		t.Fatalf("seed legacy row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy fixture: %v", err)
	}
}

func assertUpgradeOperationColumns(t *testing.T, db *sql.DB) {
	t.Helper()
	columns := map[string]bool{}
	rows, err := db.Query(`pragma table_info(upgrade_operations)`)
	if err != nil {
		t.Fatalf("inspect columns: %v", err)
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			t.Fatalf("scan column: %v", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("inspect columns: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close columns: %v", err)
	}
	for _, column := range []string{"revision", "last_forward_stage"} {
		if !columns[column] {
			t.Fatalf("upgrade_operations column %q was not added; columns = %v", column, columns)
		}
	}
}

// TestUpgradeOperationColumnsUpgradeLegacyRow covers the R2/R3 migration
// contract: an existing row gets revision 0 (so the caller that read it can
// write exactly once) and its high-water mark from the stored stage, and the
// migration is idempotent.
func TestUpgradeOperationColumnsUpgradeLegacyRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-upgrade.sqlite")
	seedLegacyUpgradeOperation(t, path, string(model.UpgradeStageVerify), `["usage_events"]`, 1000, 2000)

	ctx := context.Background()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open migrated fixture: %v", err)
	}
	assertUpgradeOperationColumns(t, db)
	repository := upgradeoperation.New(db)
	operation, found, err := repository.Get(ctx)
	if err != nil || !found {
		_ = db.Close()
		t.Fatalf("get migrated row: found=%t err=%v", found, err)
	}
	if operation.Stage != model.UpgradeStageVerify || operation.LastForwardStage != model.UpgradeStageVerify {
		_ = db.Close()
		t.Fatalf("migrated row = %q high-water %q, want verify/verify", operation.Stage, operation.LastForwardStage)
	}
	if operation.Revision != 0 {
		_ = db.Close()
		t.Fatalf("migrated revision = %d, want 0", operation.Revision)
	}
	if operation.CreatedAtMS != 1000 || operation.UpdatedAtMS != 2000 {
		_ = db.Close()
		t.Fatalf("migrated timestamps = %d/%d, want 1000/2000", operation.CreatedAtMS, operation.UpdatedAtMS)
	}
	if !operation.HasDomain(model.UpgradeDomainUsageEvents) {
		_ = db.Close()
		t.Fatalf("migrated domains = %v, want usage_events", operation.DomainsDone)
	}
	// The migrated row is now writable through the repository, and the verdict
	// it may reach is recorded by the gate: the evidence and the stage land in
	// one statement behind the revision the transaction read.
	verification := model.UpgradeVerification{
		TargetMode:          model.UpgradeTargetExternal,
		SourceFingerprint:   "sha256:source-manifest",
		Domains:             []string{model.UpgradeDomainUsageEvents},
		BindingIdentity:     "https://cpa.example",
		BindingGeneration:   "https://cpa.example",
		BindingObservedAtMS: 1001,
		ObservedAtMS:        1002,
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		_ = db.Close()
		t.Fatalf("begin completion: %v", err)
	}
	if _, err := upgradeoperation.RecordCompletionTx(ctx, tx, verification); err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		t.Fatalf("record completion on the migrated row: %v", err)
	}
	if err := tx.Commit(); err != nil {
		_ = db.Close()
		t.Fatalf("commit completion: %v", err)
	}
	committed, _, err := repository.Get(ctx)
	if err != nil {
		_ = db.Close()
		t.Fatalf("get committed row: %v", err)
	}
	if committed.Revision != 1 || committed.LastForwardStage != model.UpgradeStageComplete {
		_ = db.Close()
		t.Fatalf("committed row = revision %d high-water %q", committed.Revision, committed.LastForwardStage)
	}
	// The migrated write spent the token it read: a second write that still holds
	// revision 0 is refused instead of overwriting the verdict.
	if err := repository.Put(ctx, operation); !errors.Is(err, upgradeoperation.ErrConflict) {
		_ = db.Close()
		t.Fatalf("second write with the migrated token error = %v, want ErrConflict", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close migrated fixture: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen migrated fixture: %v", err)
	}
	defer func() { _ = db.Close() }()
	assertUpgradeOperationColumns(t, db)
	reopened, found, err := upgradeoperation.New(db).Get(ctx)
	if err != nil || !found {
		t.Fatalf("get reopened row: found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(committed, reopened) {
		t.Fatalf("reopen changed the row:\n before %+v\n after  %+v", committed, reopened)
	}
}

// TestLegacyFailedUpgradeOperationFailsClosed covers the R3 migration choice: a
// row that was already failed carries no knowable forward stage, so it is
// stored with an empty high-water mark and every resume is refused instead of
// inventing progress. Staying failed remains possible.
func TestLegacyFailedUpgradeOperationFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-failed-upgrade.sqlite")
	seedLegacyUpgradeOperation(t, path, string(model.UpgradeStageFailed), `["usage_events"]`, 1000, 2000)

	ctx := context.Background()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open migrated fixture: %v", err)
	}
	defer func() { _ = db.Close() }()
	assertUpgradeOperationColumns(t, db)
	repository := upgradeoperation.New(db)
	parked, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get migrated failed row: found=%t err=%v", found, err)
	}
	if parked.Stage != model.UpgradeStageFailed || parked.LastForwardStage != "" || parked.Revision != 0 {
		t.Fatalf("migrated failed row = %q high-water %q revision %d", parked.Stage, parked.LastForwardStage, parked.Revision)
	}
	if _, err := repository.Advance(ctx, "", model.UpgradeStageVerify); !errors.Is(err, upgradeoperation.ErrStageRegression) {
		t.Fatalf("advance resume error = %v, want ErrStageRegression", err)
	}
	resume := parked
	resume.Stage = model.UpgradeStageVerify
	if err := repository.Put(ctx, resume); !errors.Is(err, upgradeoperation.ErrStageRegression) {
		t.Fatalf("put resume error = %v, want ErrStageRegression", err)
	}
	unchanged, found, err := repository.Get(ctx)
	if err != nil || !found {
		t.Fatalf("get after refused resumes: found=%t err=%v", found, err)
	}
	if !reflect.DeepEqual(parked, unchanged) {
		t.Fatalf("refused resume changed the row:\n before %+v\n after  %+v", parked, unchanged)
	}
	parked.ErrorCode = "copy_in_failed"
	if err := repository.Put(ctx, parked); err != nil {
		t.Fatalf("rewrite failed row: %v", err)
	}
	rewritten, _, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("get rewritten failed row: %v", err)
	}
	if rewritten.Stage != model.UpgradeStageFailed || rewritten.LastForwardStage != "" || rewritten.Revision != 1 {
		t.Fatalf("rewritten failed row = %q high-water %q revision %d", rewritten.Stage, rewritten.LastForwardStage, rewritten.Revision)
	}
}
