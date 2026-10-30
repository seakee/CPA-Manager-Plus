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

const selectColumns = `stage, domains_done, source_kind, source_version,
	source_fingerprint_before, source_fingerprint_after, backup_manifest_sha256,
	reconciliation, manual_items, error_code, error_detail, created_at_ms, updated_at_ms`

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
		op                                     model.UpgradeOperation
		stage, domains, reconciliation         string
		manualItems                            string
		sourceKind, sourceVersion              sql.NullString
		fingerprintBefore, fingerprintAfter    sql.NullString
		backupManifest, errorCode, errorDetail sql.NullString
	)
	err := row.Scan(&stage, &domains, &sourceKind, &sourceVersion,
		&fingerprintBefore, &fingerprintAfter, &backupManifest,
		&reconciliation, &manualItems, &errorCode, &errorDetail,
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
	op.SourceFingerprintBefore = fingerprintBefore.String
	op.SourceFingerprintAfter = fingerprintAfter.String
	op.BackupManifestSHA256 = backupManifest.String
	op.ErrorCode = errorCode.String
	op.ErrorDetail = errorDetail.String
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
	_, err = writer.ExecContext(ctx, `insert into `+tableName+` (
		id, stage, domains_done, source_kind, source_version,
		source_fingerprint_before, source_fingerprint_after, backup_manifest_sha256,
		reconciliation, manual_items, error_code, error_detail, created_at_ms, updated_at_ms
	) values (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	on conflict(id) do update set
		stage = excluded.stage,
		domains_done = excluded.domains_done,
		source_kind = excluded.source_kind,
		source_version = excluded.source_version,
		source_fingerprint_before = excluded.source_fingerprint_before,
		source_fingerprint_after = excluded.source_fingerprint_after,
		backup_manifest_sha256 = excluded.backup_manifest_sha256,
		reconciliation = excluded.reconciliation,
		manual_items = excluded.manual_items,
		error_code = excluded.error_code,
		error_detail = excluded.error_detail,
		created_at_ms = excluded.created_at_ms,
		updated_at_ms = excluded.updated_at_ms`,
		string(op.Stage), string(domains), nullString(op.SourceKind), nullString(op.SourceVersion),
		nullString(op.SourceFingerprintBefore), nullString(op.SourceFingerprintAfter),
		nullString(op.BackupManifestSHA256), string(op.Reconciliation), string(manualItems),
		nullString(op.ErrorCode), nullString(op.ErrorDetail), op.CreatedAtMS, op.UpdatedAtMS)
	return err
}

// Tx is the transaction surface the recording helpers need, so a caller can
// commit a stage change together with the evidence that justifies it.
type Tx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Advance records a completed domain and moves the stage in one transaction.
// Domains are append-only and the stage order is forward-only; an attempt to
// go backwards returns ErrStageRegression and leaves the row untouched.
// Advancing does not imply that later stages are authorized.
func (r *Repository) Advance(ctx context.Context, domain string, stage model.UpgradeStage) (model.UpgradeOperation, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	advanced, err := AdvanceTx(ctx, tx, domain, stage)
	if err != nil {
		// A regression still reports the row it refused to move.
		return advanced, err
	}
	if err := tx.Commit(); err != nil {
		return model.UpgradeOperation{}, err
	}
	return advanced, nil
}

// AdvanceTx records a completed domain and moves the stage inside the caller's
// transaction, so the stage can only ever commit beside the evidence that
// explains it. The rules are the ones Advance documents.
func AdvanceTx(ctx context.Context, tx Tx, domain string, stage model.UpgradeStage) (model.UpgradeOperation, error) {
	if domain != "" && !model.ValidUpgradeDomain(domain) {
		return model.UpgradeOperation{}, fmt.Errorf("%w: %q", ErrUnknownDomain, domain)
	}
	if !stage.Valid() {
		return model.UpgradeOperation{}, fmt.Errorf("%w: %q", ErrUnknownStage, stage)
	}
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
	return current, nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// RecordReconciliation stores the reconciliation report on the single row
// without rebuilding it: Put would clear the provenance a completed operation
// must keep carrying, so this is the only writer for this column. A row must
// already exist, because a reconciliation only ever describes a recorded
// copy-in.
func (r *Repository) RecordReconciliation(ctx context.Context, report json.RawMessage) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := RecordReconciliationTx(ctx, tx, report); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordReconciliationTx stores the report inside the caller's transaction, so
// the evidence commits together with the marks it explains and the stage it
// authorizes.
func RecordReconciliationTx(ctx context.Context, tx Tx, report json.RawMessage) error {
	if len(report) == 0 {
		report = json.RawMessage("{}")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(report, &object); err != nil {
		return fmt.Errorf("%w: reconciliation must be a JSON object", ErrInvalidRow)
	}
	result, err := tx.ExecContext(ctx, `update `+tableName+`
		set reconciliation = ?, updated_at_ms = ? where id = 1`, string(report), time.Now().UnixMilli())
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: no operation row to record the reconciliation on", ErrInvalidRow)
	}
	return nil
}
