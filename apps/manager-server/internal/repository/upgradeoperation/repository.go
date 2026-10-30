// Package upgradeoperation persists the single v2 Upgrade operation row.
//
// The operation is one row (id = 1): its stage, the domains already completed,
// the source observations taken before and after the copy, the backup manifest
// reference, reconciliation output and manual items. This package stores
// progress; it grants no authority. A row is never created inside a legacy
// source root, and stage movement is monotonic so a retry cannot make recorded
// work look uncompleted.
package upgradeoperation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

const tableName = "upgrade_operations"

var (
	// ErrStageRegression reports an attempt to move the operation backwards.
	// The attempted write is rejected without touching the row.
	ErrStageRegression = errors.New("upgrade operation stage cannot move backwards")
	ErrUnknownDomain   = errors.New("upgrade operation domain is not supported")
	ErrUnknownStage    = errors.New("upgrade operation stage is not supported")
	ErrInvalidRow      = errors.New("upgrade operation row is invalid")
)

type Repository struct{ db *sql.DB }

func New(db *sql.DB) *Repository { return &Repository{db: db} }

const selectColumns = `stage, domains_done, source_kind, source_version, source_root,
	source_fingerprint_before, source_fingerprint_after, backup_manifest_sha256,
	reconciliation, manual_items, error_code, error_detail,
	rollback_image, rollback_status_url, rollback_source_fingerprint, rollback_observed_at_ms,
	created_at_ms, updated_at_ms`

// Get returns the single operation row. found is false before the first
// Advance; the caller must not treat that as an operation in progress.
func (r *Repository) Get(ctx context.Context) (model.UpgradeOperation, bool, error) {
	row := r.db.QueryRowContext(ctx, `select `+selectColumns+` from `+tableName+` where id = 1`)
	return scanOperation(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanOperation(row rowScanner) (model.UpgradeOperation, bool, error) {
	var (
		op                                            model.UpgradeOperation
		stage, domains, reconciliation                string
		manualItems                                   string
		sourceKind, sourceVersion, sourceRoot         sql.NullString
		fingerprintBefore, fingerprintAfter           sql.NullString
		backupManifest, errorCode, errorDetail        sql.NullString
		rollbackImage, rollbackStatusURL, rollbackFpr sql.NullString
		rollbackObservedAtMS                          sql.NullInt64
	)
	err := row.Scan(&stage, &domains, &sourceKind, &sourceVersion, &sourceRoot,
		&fingerprintBefore, &fingerprintAfter, &backupManifest,
		&reconciliation, &manualItems, &errorCode, &errorDetail,
		&rollbackImage, &rollbackStatusURL, &rollbackFpr, &rollbackObservedAtMS,
		&op.CreatedAtMS, &op.UpdatedAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		return model.UpgradeOperation{}, false, nil
	}
	if err != nil {
		return model.UpgradeOperation{}, false, err
	}
	op.Stage = model.UpgradeStage(stage)
	op.SourceKind = sourceKind.String
	op.SourceVersion = sourceVersion.String
	op.SourceRoot = sourceRoot.String
	op.SourceFingerprintBefore = fingerprintBefore.String
	op.SourceFingerprintAfter = fingerprintAfter.String
	op.BackupManifestSHA256 = backupManifest.String
	op.ErrorCode = errorCode.String
	op.ErrorDetail = errorDetail.String
	if rollbackImage.String != "" || rollbackStatusURL.String != "" || rollbackFpr.String != "" || rollbackObservedAtMS.Int64 > 0 {
		op.Rollback = &model.RollbackEvidence{
			Image:             rollbackImage.String,
			StatusURL:         rollbackStatusURL.String,
			SourceFingerprint: rollbackFpr.String,
			ObservedAtMS:      rollbackObservedAtMS.Int64,
		}
	}
	if op.DomainsDone, err = decodeStringList(domains, "domains_done"); err != nil {
		return model.UpgradeOperation{}, false, err
	}
	if op.ManualItems, err = decodeStringList(manualItems, "manual_items"); err != nil {
		return model.UpgradeOperation{}, false, err
	}
	if reconciliation == "" {
		reconciliation = "{}"
	}
	op.Reconciliation = json.RawMessage(reconciliation)
	op.Normalize()
	if err := op.Validate(); err != nil {
		return model.UpgradeOperation{}, false, fmt.Errorf("%w: %v", ErrInvalidRow, err)
	}
	return op, true, nil
}

func decodeStringList(raw string, column string) ([]string, error) {
	values := []string{}
	if raw == "" {
		return values, nil
	}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("%w: %s is not a JSON string array", ErrInvalidRow, column)
	}
	if values == nil {
		values = []string{}
	}
	return values, nil
}

// Put replaces the single row. A zero CreatedAtMS keeps the recorded creation
// time of an existing row, so re-writing progress never rewrites history.
func (r *Repository) Put(ctx context.Context, op model.UpgradeOperation) error {
	op.Normalize()
	if err := op.Validate(); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	if op.UpdatedAtMS == 0 {
		op.UpdatedAtMS = now
	}
	if op.CreatedAtMS == 0 {
		// Resolve the creation time in the same transaction as the write so a
		// concurrent advance cannot be overwritten with a later creation time.
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		var existing int64
		switch err := tx.QueryRowContext(ctx, `select created_at_ms from `+tableName+` where id = 1`).Scan(&existing); {
		case errors.Is(err, sql.ErrNoRows), err == nil && existing <= 0:
			op.CreatedAtMS = now
		case err != nil:
			return err
		default:
			op.CreatedAtMS = existing
		}
		if err := writeOperation(ctx, tx, op); err != nil {
			return err
		}
		return tx.Commit()
	}
	return writeOperation(ctx, r.db, op)
}

type operationWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func writeOperation(ctx context.Context, writer operationWriter, op model.UpgradeOperation) error {
	domains, err := json.Marshal(op.DomainsDone)
	if err != nil {
		return err
	}
	manualItems, err := json.Marshal(op.ManualItems)
	if err != nil {
		return err
	}
	var rollbackImage, rollbackStatusURL, rollbackFingerprint any
	var rollbackObservedAtMS any
	if op.Rollback != nil {
		rollbackImage = nullString(op.Rollback.Image)
		rollbackStatusURL = nullString(op.Rollback.StatusURL)
		rollbackFingerprint = nullString(op.Rollback.SourceFingerprint)
		if op.Rollback.ObservedAtMS > 0 {
			rollbackObservedAtMS = op.Rollback.ObservedAtMS
		}
	}
	_, err = writer.ExecContext(ctx, `insert into `+tableName+` (
		id, stage, domains_done, source_kind, source_version, source_root,
		source_fingerprint_before, source_fingerprint_after, backup_manifest_sha256,
		reconciliation, manual_items, error_code, error_detail,
		rollback_image, rollback_status_url, rollback_source_fingerprint, rollback_observed_at_ms,
		created_at_ms, updated_at_ms
	) values (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	on conflict(id) do update set
		stage = excluded.stage,
		domains_done = excluded.domains_done,
		source_kind = excluded.source_kind,
		source_version = excluded.source_version,
		source_root = excluded.source_root,
		source_fingerprint_before = excluded.source_fingerprint_before,
		source_fingerprint_after = excluded.source_fingerprint_after,
		backup_manifest_sha256 = excluded.backup_manifest_sha256,
		reconciliation = excluded.reconciliation,
		manual_items = excluded.manual_items,
		error_code = excluded.error_code,
		error_detail = excluded.error_detail,
		rollback_image = excluded.rollback_image,
		rollback_status_url = excluded.rollback_status_url,
		rollback_source_fingerprint = excluded.rollback_source_fingerprint,
		rollback_observed_at_ms = excluded.rollback_observed_at_ms,
		created_at_ms = excluded.created_at_ms,
		updated_at_ms = excluded.updated_at_ms`,
		string(op.Stage), string(domains), nullString(op.SourceKind), nullString(op.SourceVersion),
		nullString(op.SourceRoot),
		nullString(op.SourceFingerprintBefore), nullString(op.SourceFingerprintAfter),
		nullString(op.BackupManifestSHA256), string(op.Reconciliation), string(manualItems),
		nullString(op.ErrorCode), nullString(op.ErrorDetail),
		rollbackImage, rollbackStatusURL, rollbackFingerprint, rollbackObservedAtMS,
		op.CreatedAtMS, op.UpdatedAtMS)
	return err
}

// Advance records a completed domain and moves the stage in one transaction.
// Domains are append-only and the stage order is forward-only; an attempt to
// go backwards returns ErrStageRegression and leaves the row untouched.
// Advancing does not imply that later stages are authorized.
func (r *Repository) Advance(ctx context.Context, domain string, stage model.UpgradeStage) (model.UpgradeOperation, error) {
	if domain != "" && !model.ValidUpgradeDomain(domain) {
		return model.UpgradeOperation{}, fmt.Errorf("%w: %q", ErrUnknownDomain, domain)
	}
	if !stage.Valid() {
		return model.UpgradeOperation{}, fmt.Errorf("%w: %q", ErrUnknownStage, stage)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	// Take the write lock before reading so two advances cannot interleave and
	// so the read below observes this transaction's own epoch.
	if _, err := tx.ExecContext(ctx, `insert into `+tableName+`
		(id, stage, created_at_ms, updated_at_ms) values (1, ?, ?, ?)
		on conflict(id) do nothing`, string(stage), now, now); err != nil {
		return model.UpgradeOperation{}, err
	}
	row := tx.QueryRowContext(ctx, `select `+selectColumns+` from `+tableName+` where id = 1`)
	current, found, err := scanOperation(row)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if !found {
		return model.UpgradeOperation{}, errors.New("upgrade operation row disappeared during advance")
	}
	if current.Stage != "" && current.Stage.Valid() && !current.Stage.CanAdvanceTo(stage) {
		return current, fmt.Errorf("%w: %q -> %q", ErrStageRegression, current.Stage, stage)
	}
	current.Stage = stage
	if domain != "" {
		current.MarkDomain(domain)
	}
	current.UpdatedAtMS = now
	if current.CreatedAtMS == 0 {
		current.CreatedAtMS = now
	}
	domains, err := json.Marshal(current.DomainsDone)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if _, err := tx.ExecContext(ctx, `update `+tableName+`
		set stage = ?, domains_done = ?, created_at_ms = ?, updated_at_ms = ?
		where id = 1`, string(current.Stage), string(domains), current.CreatedAtMS, current.UpdatedAtMS); err != nil {
		return model.UpgradeOperation{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.UpgradeOperation{}, err
	}
	return current, nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// RecordRollback writes the rolled_back stage together with the observation
// that justified it, in one transaction. It never rewrites provenance: the
// source kind, the source root, both fingerprints, the backup manifest digest
// and the completed domains are left exactly as recorded, so the evidence of
// what was rolled back survives the verdict. A row that is already rolled back
// is refused rather than re-described, and a rendered verdict is impossible:
// either the stage and its evidence both land or neither does.
func (r *Repository) RecordRollback(ctx context.Context, evidence model.RollbackEvidence) (model.UpgradeOperation, error) {
	op := model.UpgradeOperation{Stage: model.UpgradeStageRolledBack, Rollback: &evidence}
	op.Normalize()
	if err := op.Validate(); err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("%w: %v", ErrInvalidRow, err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	// Take the write lock before reading so two verdicts cannot interleave. A
	// row created here is a verdict without provenance: the deferred rollback
	// removes it.
	inserted, err := tx.ExecContext(ctx, `insert into `+tableName+`
		(id, stage, created_at_ms, updated_at_ms) values (1, ?, ?, ?)
		on conflict(id) do nothing`, string(model.UpgradeStageRolledBack), now, now)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if created, err := inserted.RowsAffected(); err == nil && created > 0 {
		return model.UpgradeOperation{}, errors.New("no upgrade operation is recorded, so there is nothing to roll back")
	}
	current, found, err := scanOperation(tx.QueryRowContext(ctx,
		`select `+selectColumns+` from `+tableName+` where id = 1`))
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if !found {
		return model.UpgradeOperation{}, errors.New("upgrade operation row disappeared during record")
	}
	if current.Stage == model.UpgradeStageRolledBack {
		return current, fmt.Errorf("%w: %q -> %q", ErrStageRegression, current.Stage, model.UpgradeStageRolledBack)
	}
	if current.Stage != "" && current.Stage.Valid() && !current.Stage.CanAdvanceTo(model.UpgradeStageRolledBack) {
		return current, fmt.Errorf("%w: %q -> %q", ErrStageRegression, current.Stage, model.UpgradeStageRolledBack)
	}
	current.Stage = model.UpgradeStageRolledBack
	current.Rollback = &evidence
	current.UpdatedAtMS = now
	if current.CreatedAtMS == 0 {
		current.CreatedAtMS = now
	}
	if _, err := tx.ExecContext(ctx, `update `+tableName+`
		set stage = ?, rollback_image = ?, rollback_status_url = ?,
			rollback_source_fingerprint = ?, rollback_observed_at_ms = ?,
			created_at_ms = ?, updated_at_ms = ?
		where id = 1`,
		string(current.Stage), evidence.Image, evidence.StatusURL,
		evidence.SourceFingerprint, evidence.ObservedAtMS,
		current.CreatedAtMS, current.UpdatedAtMS); err != nil {
		return model.UpgradeOperation{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.UpgradeOperation{}, err
	}
	return current, nil
}
