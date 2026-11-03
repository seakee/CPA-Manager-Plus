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
	// ErrStageRegression reports an attempt to move the operation backwards,
	// including a resume below the forward stage a failed operation reached.
	// The attempted write is rejected without touching the row.
	ErrStageRegression = errors.New("upgrade operation stage cannot move backwards")
	ErrDomainCleared   = errors.New("upgrade operation domain cannot be removed")
	ErrUnknownDomain   = errors.New("upgrade operation domain is not supported")
	ErrUnknownStage    = errors.New("upgrade operation stage is not supported")
	ErrInvalidRow      = errors.New("upgrade operation row is invalid")
	// ErrConflict reports a Put whose Revision no longer matches the stored row:
	// another writer committed after the caller read it. The row is untouched.
	ErrConflict = errors.New("upgrade operation changed since it was read")
)

type Repository struct{ db *sql.DB }

func New(db *sql.DB) *Repository { return &Repository{db: db} }

const selectColumns = `stage, last_forward_stage, revision, domains_done, source_kind, source_version,
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
		stage, lastForwardStage                string
		domains, reconciliation                string
		manualItems                            string
		sourceKind, sourceVersion              sql.NullString
		fingerprintBefore, fingerprintAfter    sql.NullString
		backupManifest, errorCode, errorDetail sql.NullString
	)
	err := row.Scan(&stage, &lastForwardStage, &op.Revision, &domains, &sourceKind, &sourceVersion,
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
	op.LastForwardStage = model.UpgradeStage(lastForwardStage)
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
	// Validate the stored row before normalization. A duplicate domain, an
	// unknown domain or a non-object reconciliation on disk is corruption the
	// caller must see as ErrInvalidRow; normalizing first would silently turn
	// it into an acceptable row.
	if err := op.Validate(); err != nil {
		return model.UpgradeOperation{}, false, fmt.Errorf("%w: %v", ErrInvalidRow, err)
	}
	op.Normalize()
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

// Put replaces the single row exactly once per read. op.Revision is the
// compare-and-set token: it must equal the revision Get returned, and a caller
// whose revision is stale gets ErrConflict with the stored row untouched. A
// successful Put stores revision+1, so two writes in the same millisecond stay
// distinguishable; updated_at_ms is not a token.
//
// Progress rules still apply on top of the token. The stage may only advance
// (or park in failed; a failed row may only resume at or above the forward
// stage it reached), every recorded domain must still be present, and the
// repository owns created_at_ms, updated_at_ms and LastForwardStage: caller
// proposals for those fields are dropped before validation.
//
// Precedence: typed input errors (unknown stage/domain, duplicate domain,
// non-object reconciliation) are reported before the row is read; a stale
// revision is reported before progress errors, because comparing a stale
// caller against a row it never observed would misreport the cause. Timestamps
// are reset before validation because the caller's values are discarded, not
// judged.
func (r *Repository) Put(ctx context.Context, op model.UpgradeOperation) error {
	op.CreatedAtMS = 0
	op.UpdatedAtMS = 0
	if op.Stage == model.UpgradeStageFailed {
		op.LastForwardStage = ""
	} else {
		op.LastForwardStage = op.Stage
	}
	// A bad stage or domain is reported with the same sentinels Advance uses, so
	// a caller can classify input errors from either write API with errors.Is.
	if !op.Stage.Valid() {
		return fmt.Errorf("%w: %q", ErrUnknownStage, op.Stage)
	}
	for _, domain := range op.DomainsDone {
		if !model.ValidUpgradeDomain(domain) {
			return fmt.Errorf("%w: %q", ErrUnknownDomain, domain)
		}
	}
	// Validate before Normalize, so a duplicate domain is an error instead of a
	// silently deduplicated list.
	if err := op.Validate(); err != nil {
		return err
	}
	op.Normalize()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	// Take the write lock before reading so a concurrent advance cannot be
	// overwritten with an older stage or a shorter domain list, and so the
	// revision compared below is the one this transaction will replace.
	if _, err := tx.ExecContext(ctx, `insert into `+tableName+`
		(id, stage, last_forward_stage, created_at_ms, updated_at_ms) values (1, ?, ?, ?, ?)
		on conflict(id) do nothing`, string(op.Stage), string(op.LastForwardStage), now, now); err != nil {
		return err
	}
	current, found, err := scanOperation(tx.QueryRowContext(ctx, `select `+selectColumns+` from `+tableName+` where id = 1`))
	if err != nil {
		return err
	}
	if !found {
		return errors.New("upgrade operation row disappeared during put")
	}
	if op.Revision != current.Revision {
		return fmt.Errorf("%w: expected revision %d, stored revision %d", ErrConflict, op.Revision, current.Revision)
	}
	if err := requireTransition(current, op.Stage); err != nil {
		return err
	}
	for _, domain := range current.DomainsDone {
		if !op.HasDomain(domain) {
			return fmt.Errorf("%w: %q", ErrDomainCleared, domain)
		}
	}
	op.CreatedAtMS = current.CreatedAtMS
	if op.CreatedAtMS == 0 {
		op.CreatedAtMS = now
	}
	op.UpdatedAtMS = nextUpdatedAtMS(now, current)
	op.Revision = current.Revision + 1
	if op.Stage == model.UpgradeStageFailed {
		// Parking keeps the highest forward stage the row ever reached; a
		// caller cannot replace it with a lower one.
		op.LastForwardStage = current.HighWater()
	}
	// Revalidate the row this transaction is about to persist, not just the
	// caller's input: the repository-owned fields above are rewritten here.
	if err := op.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRow, err)
	}
	if err := writeOperation(ctx, tx, op); err != nil {
		return err
	}
	return tx.Commit()
}

// requireTransition enforces forward-only progress, including the high-water
// rule for a parked row. current is the row read inside the write transaction.
// A failed row with no recorded forward stage cannot resume: its position is
// unknowable, so leaving failed is refused instead of inventing progress.
func requireTransition(current model.UpgradeOperation, next model.UpgradeStage) error {
	switch {
	case current.Stage == model.UpgradeStageFailed:
		if current.CanLeaveFailedTo(next) {
			return nil
		}
		if current.HighWater() == "" {
			return fmt.Errorf("%w: failed operation has no recorded forward stage, cannot resume at %q", ErrStageRegression, next)
		}
		return fmt.Errorf("%w: failed high-water %q -> %q", ErrStageRegression, current.HighWater(), next)
	case !current.Stage.CanAdvanceTo(next):
		return fmt.Errorf("%w: %q -> %q", ErrStageRegression, current.Stage, next)
	default:
		return nil
	}
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
		id, stage, last_forward_stage, revision, domains_done, source_kind, source_version,
		source_fingerprint_before, source_fingerprint_after, backup_manifest_sha256,
		reconciliation, manual_items, error_code, error_detail, created_at_ms, updated_at_ms
	) values (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	on conflict(id) do update set
		stage = excluded.stage,
		last_forward_stage = excluded.last_forward_stage,
		revision = excluded.revision,
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
		string(op.Stage), string(op.LastForwardStage), op.Revision, string(domains), nullString(op.SourceKind), nullString(op.SourceVersion),
		nullString(op.SourceFingerprintBefore), nullString(op.SourceFingerprintAfter),
		nullString(op.BackupManifestSHA256), string(op.Reconciliation), string(manualItems),
		nullString(op.ErrorCode), nullString(op.ErrorDetail), op.CreatedAtMS, op.UpdatedAtMS)
	return err
}

// nextUpdatedAtMS returns the updated_at_ms the next write must store. nowMS is
// the wall clock and stored is the row currently on disk (the zero operation
// before the first write).
//
// The repository owns this column: the clock and the stored row are the only
// inputs, so a caller proposal (a stale value or a unit mistake such as
// MaxInt64 from a microsecond value) can never be pinned onto the row. The
// result is never lower than the stored updated_at_ms or created_at_ms, so a
// clock that moved backwards or a row restored from a backup written later can
// never produce a row whose updated_at_ms precedes its created_at_ms. Get
// rejects such a row with ErrInvalidRow and every later reader (status, the next
// advance) would be blocked by it. Every writer of updated_at_ms must call this
// helper instead of stamping its own clock.
func nextUpdatedAtMS(nowMS int64, stored model.UpgradeOperation) int64 {
	next := nowMS
	for _, candidate := range [...]int64{stored.CreatedAtMS, stored.UpdatedAtMS} {
		if candidate > next {
			next = candidate
		}
	}
	return next
}

// Advance records a completed domain and moves the stage in one transaction.
// Domains are append-only; re-advancing an already recorded domain is a no-op,
// while a duplicate passed to Put is an input error. The stage order is
// forward-only: going backwards returns ErrStageRegression and leaves the row
// untouched, and a failed row may only resume at or above the forward stage it
// reached (see requireTransition). Every committed advance increments revision,
// so a Put that read the previous revision is rejected with ErrConflict.
// updated_at_ms only moves forward (see nextUpdatedAtMS) and the row is
// revalidated inside the transaction, so advancing a row written by a clock that
// ran ahead still leaves a row Get accepts.
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
	lastForwardStage := stage
	if stage == model.UpgradeStageFailed {
		lastForwardStage = ""
	}
	// Take the write lock before reading so two advances cannot interleave and
	// so the read below observes this transaction's own epoch.
	if _, err := tx.ExecContext(ctx, `insert into `+tableName+`
		(id, stage, last_forward_stage, created_at_ms, updated_at_ms) values (1, ?, ?, ?, ?)
		on conflict(id) do nothing`, string(stage), string(lastForwardStage), now, now); err != nil {
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
	highWater := current.HighWater()
	if err := requireTransition(current, stage); err != nil {
		return current, err
	}
	current.Stage = stage
	if stage != model.UpgradeStageFailed {
		current.LastForwardStage = stage
	} else {
		current.LastForwardStage = highWater
	}
	if domain != "" {
		current.MarkDomain(domain)
	}
	if current.CreatedAtMS == 0 {
		current.CreatedAtMS = now
	}
	current.UpdatedAtMS = nextUpdatedAtMS(now, current)
	current.Revision++
	// Validate the row this transaction is about to write. A row whose stored
	// clock ran ahead must stay readable after the advance, and Get rejects any
	// row that Validate does not accept.
	if err := current.Validate(); err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("%w: %v", ErrInvalidRow, err)
	}
	domains, err := json.Marshal(current.DomainsDone)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if _, err := tx.ExecContext(ctx, `update `+tableName+`
		set stage = ?, last_forward_stage = ?, revision = ?, domains_done = ?, created_at_ms = ?, updated_at_ms = ?
		where id = 1`, string(current.Stage), string(current.LastForwardStage), current.Revision, string(domains), current.CreatedAtMS, current.UpdatedAtMS); err != nil {
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
