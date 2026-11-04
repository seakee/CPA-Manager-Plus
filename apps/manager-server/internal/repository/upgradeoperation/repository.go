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
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
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
	// ErrRollbackEvidenceRequired reports an attempt to write the terminal
	// rolled_back stage without the observation that justified it. The verdict
	// is recorded once, with its evidence, and never as a bare stage.
	ErrRollbackEvidenceRequired = errors.New("a rolled_back operation requires recorded evidence")
	// ErrRollbackEvidenceMissing reports a stored rolled_back row that carries
	// none of, or only part of, the observation that justified it. Both write
	// paths refuse such a row before it reaches the table, so a store that holds
	// one was written by a build that recorded the verdict without its evidence.
	// A read-only reader cannot repair it and reports the store as the outdated
	// one it is instead of as a malformed row.
	ErrRollbackEvidenceMissing = errors.New("upgrade operation rolled_back row carries no evidence")
	// ErrCompleteRequiresGate reports an attempt to write the complete stage
	// through a generic writer. Completing is the one verdict the operation has,
	// and it is only sound together with the verification evidence that justified
	// it, so Put, Advance and AdvanceTx refuse the stage itself: there is one
	// production writer of `complete`, and it is RecordCompletionTx.
	ErrCompleteRequiresGate = errors.New("complete is written only by the upgrade verification gate")
	// ErrVerificationEvidenceMissing reports a stored complete row that carries
	// none of, or only part of, the verification it rests on. Every writer below
	// refuses such a row before it reaches the table, so a store that holds one
	// was written by a build that had no gate; a reader cannot re-prove it and
	// says so instead of presenting the stage as an observation.
	ErrVerificationEvidenceMissing = errors.New("upgrade operation complete row carries no verification evidence")
)

type Repository struct{ db *sql.DB }

func New(db *sql.DB) *Repository { return &Repository{db: db} }

const selectColumns = `stage, last_forward_stage, revision, domains_done, source_kind, source_version, source_root,
	source_fingerprint_before, source_fingerprint_after, backup_manifest_sha256,
	reconciliation, verification, manual_items, error_code, error_detail,
	rollback_image, rollback_image_source, rollback_image_form,
	rollback_status_url, rollback_source_fingerprint, rollback_observed_at_ms,
	created_at_ms, updated_at_ms`

// requiredColumns are the columns selectColumns reads. The table is created by a
// writable open, so a store written by an earlier build can hold the table
// without these columns; a read-only reader cannot migrate it and must say so
// instead of letting the SQL fail.
var requiredColumns = []string{
	"stage", "domains_done", "source_kind", "source_version", "source_root",
	"source_fingerprint_before", "source_fingerprint_after", "backup_manifest_sha256",
	"reconciliation", "verification", "manual_items", "error_code", "error_detail",
	"rollback_image", "rollback_image_source", "rollback_image_form",
	"rollback_status_url", "rollback_source_fingerprint", "rollback_observed_at_ms",
	"created_at_ms", "updated_at_ms",
}

// MissingColumns reports the columns of a readable operation row that this
// store does not have. An empty result means the row can be selected. The
// caller must have seen the table itself: a store without the table is a store
// with no recorded operation, not an outdated one.
func (r *Repository) MissingColumns(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `select name from pragma_table_info(?)`, tableName)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	present := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	missing := make([]string, 0, len(requiredColumns))
	for _, column := range requiredColumns {
		if !present[column] {
			missing = append(missing, column)
		}
	}
	return missing, nil
}

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
		op                                                    model.UpgradeOperation
		stage, lastForwardStage                               string
		domains, reconciliation, verification                 string
		manualItems                                           string
		sourceKind, sourceVersion, sourceRoot                 sql.NullString
		fingerprintBefore, fingerprintAfter                   sql.NullString
		backupManifest, errorCode, errorDetail                sql.NullString
		rollbackImage, rollbackImageSource, rollbackImageForm sql.NullString
		rollbackStatusURL, rollbackFpr                        sql.NullString
		rollbackObservedAtMS                                  sql.NullInt64
	)
	err := row.Scan(&stage, &lastForwardStage, &op.Revision, &domains, &sourceKind, &sourceVersion, &sourceRoot,
		&fingerprintBefore, &fingerprintAfter, &backupManifest,
		&reconciliation, &verification, &manualItems, &errorCode, &errorDetail,
		&rollbackImage, &rollbackImageSource, &rollbackImageForm,
		&rollbackStatusURL, &rollbackFpr, &rollbackObservedAtMS,
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
	op.SourceRoot = sourceRoot.String
	op.SourceFingerprintBefore = fingerprintBefore.String
	op.SourceFingerprintAfter = fingerprintAfter.String
	op.BackupManifestSHA256 = backupManifest.String
	op.ErrorCode = errorCode.String
	op.ErrorDetail = errorDetail.String
	if rollbackImage.String != "" || rollbackImageSource.String != "" || rollbackImageForm.String != "" ||
		rollbackStatusURL.String != "" || rollbackFpr.String != "" || rollbackObservedAtMS.Int64 > 0 {
		op.Rollback = &model.RollbackEvidence{
			Image:             rollbackImage.String,
			ImageSource:       rollbackImageSource.String,
			ImageForm:         rollbackImageForm.String,
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
	// Only construction and Put input apply defaults. An existing row must
	// carry an actual JSON object: an empty string here is corruption, not an
	// empty reconciliation, because the column is "text not null default '{}'".
	if reconciliation == "" {
		return model.UpgradeOperation{}, false, fmt.Errorf("%w: reconciliation is not a JSON object", ErrInvalidRow)
	}
	op.Reconciliation = json.RawMessage(reconciliation)
	// A verification block is a JSON object on disk, exactly like the
	// reconciliation: an empty string is corruption, not "never verified".
	if verification == "" {
		return model.UpgradeOperation{}, false, fmt.Errorf("%w: verification is not a JSON object", ErrInvalidRow)
	}
	op.Verification = json.RawMessage(verification)
	// The verdict and its evidence are written together, so a store that holds
	// the terminal stage without a whole observation was written by a build that
	// did not. Validate would report it as a malformed row; naming the case lets
	// a reader report the store the operator can act on.
	if op.Stage == model.UpgradeStageRolledBack && (op.Rollback == nil || !op.Rollback.Complete()) {
		return model.UpgradeOperation{}, false, ErrRollbackEvidenceMissing
	}
	// The completion verdict is the same kind of record: the one writer stores
	// the stage and the evidence in one statement, so a complete row without a
	// whole verification was written by a build that had no gate.
	if op.Stage == model.UpgradeStageComplete {
		if evidence, err := op.VerificationEvidence(); err != nil || !evidence.Complete() {
			return model.UpgradeOperation{}, false, ErrVerificationEvidenceMissing
		}
	}
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

// decodeStringList parses a stored JSON string array. Only construction and Put
// input may default a missing value; an existing row keeps the schema contract
// ("text not null default '[]'"), so an empty string or JSON null is corruption
// and is reported as ErrInvalidRow instead of being read as an empty list.
func decodeStringList(raw string, column string) ([]string, error) {
	trimmed := bytes.TrimSpace([]byte(raw))
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, fmt.Errorf("%w: %s is not a JSON string array", ErrInvalidRow, column)
	}
	values := []string{}
	if err := json.Unmarshal(trimmed, &values); err != nil {
		return nil, fmt.Errorf("%w: %s is not a JSON string array", ErrInvalidRow, column)
	}
	if values == nil {
		return nil, fmt.Errorf("%w: %s is not a JSON string array", ErrInvalidRow, column)
	}
	return values, nil
}

// revisionGuard checks the revision-checked statement one writer issued: a zero
// row count means the stored revision is no longer the one the write read, so
// another writer committed first and this write changed nothing. Every writer
// of the single operation row carries the revision it read and gets ErrConflict
// from this check instead of overwriting a newer row.
func revisionGuard(result sql.Result, expected int64) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: expected revision %d, the stored row is newer", ErrConflict, expected)
	}
	return nil
}

// rowCreated reports whether the guarded insert created the single row.
// "on conflict(id) do nothing" affects zero rows when the row already existed,
// which is how a first write is distinguished from a write to stored state.
func rowCreated(result sql.Result) (bool, error) {
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
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
// The complete stage is refused outright (ErrCompleteRequiresGate): it is the
// verification gate's verdict and only RecordCompletionTx may store it, together
// with the evidence it rests on. A proposal naming that stage is refused before
// the row is read; every other proposal against a stored complete row is refused
// as well (ErrStageRegression), with the row untouched, so the verdict is frozen
// once recorded. The stored verification block is repository-owned like the
// timestamps: a whole-row proposal carries it forward instead of erasing the
// evidence.
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
	if op.Stage == model.UpgradeStageFailed || op.Stage == model.UpgradeStageRolledBack {
		// A parked or rolled-back row keeps the highest forward stage it
		// reached. It is unknown before the stored row is read, so an empty
		// value stands in here and the transaction recomputes it.
		op.LastForwardStage = ""
	} else {
		op.LastForwardStage = op.Stage
	}
	// The one verdict the whole operation rests on is written by the gate, with
	// the observation that justifies it. A generic stage write cannot produce
	// that pair, so the stage itself is refused here, before the row is read.
	if op.Stage == model.UpgradeStageComplete {
		return fmt.Errorf("%w: record the verdict with RecordCompletionTx", ErrCompleteRequiresGate)
	}
	// This is one of the two paths that write a stage, so the terminal verdict's
	// evidence invariant is enforced here, before the model's shape check turns
	// it into a plain error.
	if err := requireVerdictEvidence(op); err != nil {
		return err
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
	result, err := tx.ExecContext(ctx, `insert into `+tableName+`
		(id, stage, last_forward_stage, created_at_ms, updated_at_ms) values (1, ?, ?, ?, ?)
		on conflict(id) do nothing`, string(op.Stage), string(op.LastForwardStage), now, now)
	if err != nil {
		return err
	}
	created, err := rowCreated(result)
	if err != nil {
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
	if !created {
		if current.Stage == model.UpgradeStageComplete {
			// A stored complete row is frozen: it may not gain or lose a
			// domain, and no observation may be replaced. The verdict has
			// already been recorded with its evidence by the gate, and nothing
			// this writer stores could re-prove it.
			return fmt.Errorf("%w: completed operation is frozen at %q", ErrStageRegression, current.Stage)
		}
		if err := requireTransition(current, op.Stage); err != nil {
			return err
		}
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
	// The verification block is the gate's record, never a caller's. This write
	// replaces every column, so the stored value is carried forward: erasing it
	// would leave a Managed verify row with a stage no reader could re-check.
	op.Verification = current.Verification
	op.UpdatedAtMS = nextUpdatedAtMS(now, current)
	op.Revision = current.Revision + 1
	if op.Stage == model.UpgradeStageFailed || op.Stage == model.UpgradeStageRolledBack {
		// Parking and the terminal verdict keep the highest forward stage the
		// row ever reached; a caller cannot replace it with a lower one.
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
	var rollbackImage, rollbackImageSource, rollbackImageForm any
	var rollbackStatusURL, rollbackFingerprint any
	var rollbackObservedAtMS any
	if op.Rollback != nil {
		rollbackImage = nullString(op.Rollback.Image)
		rollbackImageSource = nullString(op.Rollback.ImageSource)
		rollbackImageForm = nullString(op.Rollback.ImageForm)
		rollbackStatusURL = nullString(op.Rollback.StatusURL)
		rollbackFingerprint = nullString(op.Rollback.SourceFingerprint)
		if op.Rollback.ObservedAtMS > 0 {
			rollbackObservedAtMS = op.Rollback.ObservedAtMS
		}
	}
	verification := op.Verification
	if len(bytes.TrimSpace(verification)) == 0 {
		verification = json.RawMessage("{}")
	}
	_, err = writer.ExecContext(ctx, `insert into `+tableName+` (
		id, stage, last_forward_stage, revision, domains_done, source_kind, source_version, source_root,
		source_fingerprint_before, source_fingerprint_after, backup_manifest_sha256,
		reconciliation, verification, manual_items, error_code, error_detail,
		rollback_image, rollback_image_source, rollback_image_form,
		rollback_status_url, rollback_source_fingerprint, rollback_observed_at_ms,
		created_at_ms, updated_at_ms
	) values (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	on conflict(id) do update set
		stage = excluded.stage,
		last_forward_stage = excluded.last_forward_stage,
		revision = excluded.revision,
		domains_done = excluded.domains_done,
		source_kind = excluded.source_kind,
		source_version = excluded.source_version,
		source_root = excluded.source_root,
		source_fingerprint_before = excluded.source_fingerprint_before,
		source_fingerprint_after = excluded.source_fingerprint_after,
		backup_manifest_sha256 = excluded.backup_manifest_sha256,
		reconciliation = excluded.reconciliation,
		verification = excluded.verification,
		manual_items = excluded.manual_items,
		error_code = excluded.error_code,
		error_detail = excluded.error_detail,
		rollback_image = excluded.rollback_image,
		rollback_image_source = excluded.rollback_image_source,
		rollback_image_form = excluded.rollback_image_form,
		rollback_status_url = excluded.rollback_status_url,
		rollback_source_fingerprint = excluded.rollback_source_fingerprint,
		rollback_observed_at_ms = excluded.rollback_observed_at_ms,
		created_at_ms = excluded.created_at_ms,
		updated_at_ms = excluded.updated_at_ms`,
		string(op.Stage), string(op.LastForwardStage), op.Revision, string(domains), nullString(op.SourceKind), nullString(op.SourceVersion),
		nullString(op.SourceRoot),
		nullString(op.SourceFingerprintBefore), nullString(op.SourceFingerprintAfter),
		nullString(op.BackupManifestSHA256), string(op.Reconciliation), string(verification), string(manualItems),
		nullString(op.ErrorCode), nullString(op.ErrorDetail),
		rollbackImage, rollbackImageSource, rollbackImageForm,
		rollbackStatusURL, rollbackFingerprint, rollbackObservedAtMS,
		op.CreatedAtMS, op.UpdatedAtMS)
	return err
}

// Tx is the transaction surface the recording helpers need, so a caller can
// commit a stage change together with the evidence that justifies it.
type Tx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
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
// A stage argument naming complete is refused (ErrCompleteRequiresGate):
// completing is the verification gate's verdict, and only RecordCompletionTx may
// store it with the evidence it rests on. Every advance against a stored complete
// row is refused as well: the verdict is frozen, so ErrStageRegression comes back
// and the row is left untouched.
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
// explains it. The rules are the ones Advance documents, and the update carries
// the revision this transaction read, so an advance that lost a race to another
// writer returns ErrConflict with the row untouched.
func AdvanceTx(ctx context.Context, tx Tx, domain string, stage model.UpgradeStage) (model.UpgradeOperation, error) {
	if domain != "" && !model.ValidUpgradeDomain(domain) {
		return model.UpgradeOperation{}, fmt.Errorf("%w: %q", ErrUnknownDomain, domain)
	}
	if !stage.Valid() {
		return model.UpgradeOperation{}, fmt.Errorf("%w: %q", ErrUnknownStage, stage)
	}
	// Advance carries no observation, so it can never write the terminal
	// verdict: the evidence and the stage land together in RecordRollback.
	if stage == model.UpgradeStageRolledBack {
		return model.UpgradeOperation{}, fmt.Errorf("%w: record the verdict with its evidence instead", ErrRollbackEvidenceRequired)
	}
	// Completion is the same kind of record as the rollback verdict: the stage
	// is only sound together with the verification observation, and Advance
	// carries none.
	if stage == model.UpgradeStageComplete {
		return model.UpgradeOperation{}, fmt.Errorf("%w: record the verdict with RecordCompletionTx", ErrCompleteRequiresGate)
	}
	now := time.Now().UnixMilli()
	lastForwardStage := stage
	if stage == model.UpgradeStageFailed {
		lastForwardStage = ""
	}
	// Take the write lock before reading so two advances cannot interleave and
	// so the read below observes this transaction's own epoch.
	result, err := tx.ExecContext(ctx, `insert into `+tableName+`
		(id, stage, last_forward_stage, created_at_ms, updated_at_ms) values (1, ?, ?, ?, ?)
		on conflict(id) do nothing`, string(stage), string(lastForwardStage), now, now)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	created, err := rowCreated(result)
	if err != nil {
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
	if !created {
		if current.Stage == model.UpgradeStageComplete {
			// A stored complete row is frozen: its domains, timestamps and
			// evidence were accepted by the gate, and no later step may add to
			// or re-describe the completed operation.
			return current, fmt.Errorf("%w: completed operation is frozen at %q", ErrStageRegression, current.Stage)
		}
		if err := requireTransition(current, stage); err != nil {
			return current, err
		}
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
	expectedRevision := current.Revision
	current.Revision = expectedRevision + 1
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
	// The update carries the revision this transaction read: a row another
	// writer advanced since would not match, and this advance then reports
	// ErrConflict instead of overwriting it.
	result, err = tx.ExecContext(ctx, `update `+tableName+`
		set stage = ?, last_forward_stage = ?, revision = ?, domains_done = ?, created_at_ms = ?, updated_at_ms = ?
		where id = 1 and revision = ?`, string(current.Stage), string(current.LastForwardStage), current.Revision, string(domains), current.CreatedAtMS, current.UpdatedAtMS, expectedRevision)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if err := revisionGuard(result, expectedRevision); err != nil {
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

// RecordError records the typed refusal a step stopped on, without touching the
// stage or the completed domains: the step did not advance, and no path may make
// a refusal look like progress. Only the error columns, the revision and the
// update time move, so the provenance a later rollback relies on is left exactly
// as it was.
//
// The stamp is computed from the stored row inside the transaction, so a refusal
// written by a process whose clock lags the store cannot move updated_at_ms
// backwards, and the row is validated before it is written.
func (r *Repository) RecordError(ctx context.Context, code, detail string) error {
	if strings.TrimSpace(code) == "" {
		return fmt.Errorf("%w: the refusal code is empty", ErrInvalidRow)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	// Take the write lock before reading, so the stamp this refusal advances is
	// the one on disk. The placeholder carries no stage, and a refusal may not
	// create an operation: a row of zero rows affected means there is none.
	inserted, err := tx.ExecContext(ctx, `insert into `+tableName+`
		(id, stage, created_at_ms, updated_at_ms) values (1, ?, ?, ?)
		on conflict(id) do nothing`, "", now, now)
	if err != nil {
		return err
	}
	if created, err := inserted.RowsAffected(); err == nil && created > 0 {
		return fmt.Errorf("%w: no operation row to record the refusal on", ErrInvalidRow)
	}
	current, found, err := scanOperation(tx.QueryRowContext(ctx,
		`select `+selectColumns+` from `+tableName+` where id = 1`))
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: no operation row to record the refusal on", ErrInvalidRow)
	}
	current.ErrorCode = code
	current.ErrorDetail = strings.TrimSpace(detail)
	current.UpdatedAtMS = nextUpdatedAtMS(now, current)
	expectedRevision := current.Revision
	current.Revision = expectedRevision + 1
	if err := current.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRow, err)
	}
	result, err := tx.ExecContext(ctx, `update `+tableName+`
		set error_code = ?, error_detail = ?, revision = ?, updated_at_ms = ?
		where id = 1 and revision = ?`,
		current.ErrorCode, nullString(current.ErrorDetail), current.Revision, current.UpdatedAtMS, expectedRevision)
	if err != nil {
		return err
	}
	if err := revisionGuard(result, expectedRevision); err != nil {
		return err
	}
	return tx.Commit()
}

// AppendManualItemsTx appends operator instructions to the row inside the
// caller's transaction, so a step that cannot finish commits the instructions
// that say what it left undone beside the stage it advanced to.
//
// It appends rather than replaces and drops a line the row already carries, so a
// rerun does not duplicate its own instruction and never loses one an earlier
// step wrote: the list this transaction read is the only input. Like every other
// writer it stores revision+1 behind the revision it read, so a row another
// writer advanced since returns ErrConflict instead of being overwritten.
func AppendManualItemsTx(ctx context.Context, tx Tx, items []string) (model.UpgradeOperation, error) {
	now := time.Now().UnixMilli()
	current, found, err := scanOperation(tx.QueryRowContext(ctx,
		`select `+selectColumns+` from `+tableName+` where id = 1`))
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if !found {
		return model.UpgradeOperation{}, fmt.Errorf("%w: no operation row to record the manual items on", ErrInvalidRow)
	}
	for _, item := range items {
		line := strings.TrimSpace(item)
		if line == "" || slices.Contains(current.ManualItems, line) {
			continue
		}
		current.ManualItems = append(current.ManualItems, line)
	}
	payload, err := json.Marshal(current.ManualItems)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	current.UpdatedAtMS = nextUpdatedAtMS(now, current)
	expectedRevision := current.Revision
	current.Revision = expectedRevision + 1
	if err := current.Validate(); err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("%w: %v", ErrInvalidRow, err)
	}
	result, err := tx.ExecContext(ctx, `update `+tableName+`
		set manual_items = ?, revision = ?, updated_at_ms = ?
		where id = 1 and revision = ?`,
		string(payload), current.Revision, current.UpdatedAtMS, expectedRevision)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if err := revisionGuard(result, expectedRevision); err != nil {
		return model.UpgradeOperation{}, err
	}
	return current, nil
}

// ClearErrorTx clears a refusal inside the caller's transaction, so a step that
// succeeded can commit the cleared refusal beside the stage it advanced to. The
// refusal is the durable statement that the step did not advance; leaving it
// behind after a successful run would keep telling the wizard a stop is still
// standing. Only the error columns, the revision and the update time move, and
// the stamp is computed from the row as this transaction sees it.
func ClearErrorTx(ctx context.Context, tx Tx) error {
	now := time.Now().UnixMilli()
	current, found, err := scanOperation(tx.QueryRowContext(ctx,
		`select `+selectColumns+` from `+tableName+` where id = 1`))
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: no operation row to clear the refusal on", ErrInvalidRow)
	}
	current.ErrorCode = ""
	current.ErrorDetail = ""
	current.UpdatedAtMS = nextUpdatedAtMS(now, current)
	expectedRevision := current.Revision
	current.Revision = expectedRevision + 1
	if err := current.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRow, err)
	}
	result, err := tx.ExecContext(ctx, `update `+tableName+`
		set error_code = null, error_detail = null, revision = ?, updated_at_ms = ?
		where id = 1 and revision = ?`,
		current.Revision, current.UpdatedAtMS, expectedRevision)
	if err != nil {
		return err
	}
	return revisionGuard(result, expectedRevision)
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
// authorizes. The repository owns updated_at_ms here too: it is recomputed from
// the stored row with nextUpdatedAtMS, and the row is validated before it is
// written, in this transaction, so a clock that ran ahead cannot make the next
// reader reject the row. The write carries the revision this transaction read
// and stores one more, so it conflicts instead of overwriting a newer row.
func RecordReconciliationTx(ctx context.Context, tx Tx, report json.RawMessage) error {
	if len(report) == 0 {
		report = json.RawMessage("{}")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(report, &object); err != nil {
		return fmt.Errorf("%w: reconciliation must be a JSON object", ErrInvalidRow)
	}
	row := tx.QueryRowContext(ctx, `select `+selectColumns+` from `+tableName+` where id = 1`)
	current, found, err := scanOperation(row)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: no operation row to record the reconciliation on", ErrInvalidRow)
	}
	current.UpdatedAtMS = nextUpdatedAtMS(time.Now().UnixMilli(), current)
	expectedRevision := current.Revision
	current.Revision = expectedRevision + 1
	if err := current.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRow, err)
	}
	result, err := tx.ExecContext(ctx, `update `+tableName+`
		set reconciliation = ?, revision = ?, updated_at_ms = ?
		where id = 1 and revision = ?`, string(report), current.Revision, current.UpdatedAtMS, expectedRevision)
	if err != nil {
		return err
	}
	return revisionGuard(result, expectedRevision)
}

// RecordErrorTx records the typed refusal inside the caller's transaction, so a
// step that cannot finish commits the reason beside the position it recorded.
// Only the error columns, the revision and the update time move, and the stamp is
// computed from the row as this transaction sees it. It is the transactional
// form of RecordError and the only writer of these columns besides ClearErrorTx.
func RecordErrorTx(ctx context.Context, tx Tx, code, detail string) (model.UpgradeOperation, error) {
	if strings.TrimSpace(code) == "" {
		return model.UpgradeOperation{}, fmt.Errorf("%w: the refusal code is empty", ErrInvalidRow)
	}
	now := time.Now().UnixMilli()
	current, found, err := scanOperation(tx.QueryRowContext(ctx,
		`select `+selectColumns+` from `+tableName+` where id = 1`))
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if !found {
		return model.UpgradeOperation{}, fmt.Errorf("%w: no operation row to record the refusal on", ErrInvalidRow)
	}
	current.ErrorCode = code
	current.ErrorDetail = strings.TrimSpace(detail)
	current.UpdatedAtMS = nextUpdatedAtMS(now, current)
	expectedRevision := current.Revision
	current.Revision = expectedRevision + 1
	if err := current.Validate(); err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("%w: %v", ErrInvalidRow, err)
	}
	result, err := tx.ExecContext(ctx, `update `+tableName+`
		set error_code = ?, error_detail = ?, revision = ?, updated_at_ms = ?
		where id = 1 and revision = ?`,
		current.ErrorCode, nullString(current.ErrorDetail), current.Revision, current.UpdatedAtMS, expectedRevision)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if err := revisionGuard(result, expectedRevision); err != nil {
		return model.UpgradeOperation{}, err
	}
	return current, nil
}

// lockOperationRow takes the write lock on the single row before any gate
// comparison reads it, so the state this transaction judges is the state its
// statement replaces. The placeholder carries no stage and never creates an
// operation: a gate only ever continues one that a step already recorded.
func lockOperationRow(ctx context.Context, tx Tx, nowMS int64) error {
	inserted, err := tx.ExecContext(ctx, `insert into `+tableName+`
		(id, stage, created_at_ms, updated_at_ms) values (1, ?, ?, ?)
		on conflict(id) do nothing`, "", nowMS, nowMS)
	if err != nil {
		return err
	}
	if created, err := inserted.RowsAffected(); err == nil && created > 0 {
		return fmt.Errorf("%w: no recorded operation to verify", ErrInvalidRow)
	}
	return nil
}

// RecordCompletionTx is the one production writer of the complete stage. It
// stores the verification evidence and the stage in one statement, behind the
// revision this transaction read, so a store can never hold `complete` without
// the observation that justified it and a crash between the two is impossible.
//
// Put, Advance and AdvanceTx refuse this stage (ErrCompleteRequiresGate), so
// every other writer reaches a complete row only to find it frozen. The
// verification is validated here, before the row is read, so an incomplete
// observation is an input error rather than a stored claim. A repeated call with
// the same evidence writes nothing: the stage and its record are already on the
// row, and a second write would only move the timestamp of a verdict that did
// not change.
func RecordCompletionTx(ctx context.Context, tx Tx, verification model.UpgradeVerification) (model.UpgradeOperation, error) {
	return recordVerificationTx(ctx, tx, model.UpgradeStageComplete, verification, "", "", nil)
}

// RecordWaitingVerificationTx records the verify position a Managed operation
// reaches: the evidence this run proved, the typed reason this build may not
// promote its authority, and the operator instructions that say what remains.
// Nothing is cleared — the manual items and the refusals earlier steps recorded
// stay exactly as they were — because a position that is waiting is not a
// position that succeeded.
//
// It is not a completion: the stage is verify, which the operation can leave
// only towards complete, and only RecordCompletionTx may write that.
func RecordWaitingVerificationTx(ctx context.Context, tx Tx, verification model.UpgradeVerification, code, detail string, manualItems []string) (model.UpgradeOperation, error) {
	if strings.TrimSpace(code) == "" {
		return model.UpgradeOperation{}, fmt.Errorf("%w: the verify reason code is empty", ErrInvalidRow)
	}
	return recordVerificationTx(ctx, tx, model.UpgradeStageVerify, verification, code, detail, manualItems)
}

// recordVerificationTx is the shared body of the two gate writers: one
// statement, one revision, one timestamp, and an idempotent repeat that writes
// nothing at all.
func recordVerificationTx(ctx context.Context, tx Tx, stage model.UpgradeStage, verification model.UpgradeVerification,
	code, detail string, manualItems []string) (model.UpgradeOperation, error) {
	if stage != model.UpgradeStageComplete && stage != model.UpgradeStageVerify {
		return model.UpgradeOperation{}, fmt.Errorf("%w: the gate records only verify and complete", ErrUnknownStage)
	}
	if err := verification.Validate(); err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("%w: %v", ErrInvalidRow, err)
	}
	payload, err := json.Marshal(verification)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	now := time.Now().UnixMilli()
	if err := lockOperationRow(ctx, tx, now); err != nil {
		return model.UpgradeOperation{}, err
	}
	current, found, err := GetTx(ctx, tx)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if !found {
		return model.UpgradeOperation{}, fmt.Errorf("%w: no recorded operation to verify", ErrInvalidRow)
	}
	if current.Stage == model.UpgradeStageComplete {
		return current, fmt.Errorf("%w: completed operation is frozen at %q", ErrStageRegression, current.Stage)
	}
	if err := requireTransition(current, stage); err != nil {
		return current, err
	}
	next := current
	added := []string{}
	for _, item := range manualItems {
		line := strings.TrimSpace(item)
		if line == "" || slices.Contains(next.ManualItems, line) || slices.Contains(added, line) {
			continue
		}
		added = append(added, line)
	}
	// The repeat of the verdict already on the row stores nothing: not the
	// evidence, not a revision, not a timestamp. A run only observes again, so
	// the observation time is the one field a repeat may change; every other
	// field says what was proven and a difference there is a new record.
	if current.Stage == stage && current.ErrorCode == code &&
		current.ErrorDetail == strings.TrimSpace(detail) && len(added) == 0 {
		if stored, decodeErr := current.VerificationEvidence(); decodeErr == nil && stored.SameVerdict(verification) {
			return current, nil
		}
	}
	next.Stage = stage
	next.LastForwardStage = stage
	next.Verification = payload
	next.ErrorCode = code
	next.ErrorDetail = strings.TrimSpace(detail)
	next.ManualItems = append(next.ManualItems, added...)
	next.UpdatedAtMS = nextUpdatedAtMS(now, next)
	expectedRevision := next.Revision
	next.Revision = expectedRevision + 1
	if err := next.Validate(); err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("%w: %v", ErrInvalidRow, err)
	}
	manual, err := json.Marshal(next.ManualItems)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	result, err := tx.ExecContext(ctx, `update `+tableName+`
		set stage = ?, last_forward_stage = ?, verification = ?, manual_items = ?,
			error_code = ?, error_detail = ?, revision = ?, updated_at_ms = ?
		where id = 1 and revision = ?`,
		string(next.Stage), string(next.LastForwardStage), string(payload), string(manual),
		nullString(next.ErrorCode), nullString(next.ErrorDetail), next.Revision, next.UpdatedAtMS, expectedRevision)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if err := revisionGuard(result, expectedRevision); err != nil {
		return model.UpgradeOperation{}, err
	}
	return next, nil
}

// requireVerdictEvidence keeps the terminal verdict and its observation
// inseparable: rolled_back is never resumed, so a row without its evidence could
// never be re-proven.
func requireVerdictEvidence(op model.UpgradeOperation) error {
	if op.Stage == model.UpgradeStageRolledBack && op.Rollback == nil {
		return fmt.Errorf("%w: write rolled_back with RollbackEvidence", ErrRollbackEvidenceRequired)
	}
	return nil
}

// RecordSourceRoot backfills the 1.x data directory of a row that recorded none:
// an operation written before the column existed still has to bind its rollback
// to the volume it copied. It only fills an empty value, so provenance is never
// rewritten, and it refuses a row that has no operation at all. The backfill is
// revision-checked like every other writer, so it cannot land beside a newer row.
func (r *Repository) RecordSourceRoot(ctx context.Context, sourceRoot string) (model.UpgradeOperation, error) {
	if strings.TrimSpace(sourceRoot) == "" {
		return model.UpgradeOperation{}, fmt.Errorf("%w: the 1.x data directory is empty", ErrInvalidRow)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, found, err := scanOperation(tx.QueryRowContext(ctx,
		`select `+selectColumns+` from `+tableName+` where id = 1`))
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if !found {
		return model.UpgradeOperation{}, errors.New("no upgrade operation is recorded, so there is no source root to record")
	}
	if current.SourceRoot != "" {
		// Another path recorded it first, so this one changed nothing.
		return current, tx.Commit()
	}
	now := time.Now().UnixMilli()
	current.SourceRoot = sourceRoot
	current.UpdatedAtMS = nextUpdatedAtMS(now, current)
	if current.CreatedAtMS == 0 {
		current.CreatedAtMS = now
	}
	expectedRevision := current.Revision
	current.Revision = expectedRevision + 1
	// Validate the row this transaction is about to persist: the clock above is
	// rewritten here, and a row whose stored clock ran ahead must stay readable.
	if err := current.Validate(); err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("%w: %v", ErrInvalidRow, err)
	}
	result, err := tx.ExecContext(ctx, `update `+tableName+`
		set source_root = ?, revision = ?, updated_at_ms = ?
		where id = 1 and revision = ?`,
		sourceRoot, current.Revision, current.UpdatedAtMS, expectedRevision)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if err := revisionGuard(result, expectedRevision); err != nil {
		return model.UpgradeOperation{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.UpgradeOperation{}, err
	}
	return current, nil
}

// RecordRollback writes the rolled_back stage together with the observation
// that justified it, in one transaction. It never rewrites provenance: the
// source kind, the source root, both fingerprints, the backup manifest digest
// and the completed domains are left exactly as recorded, so the evidence of
// what was rolled back survives the verdict. A row that is already rolled back
// is refused rather than re-described, and a rendered verdict is impossible:
// either the stage and its evidence both land or neither does. The verdict is
// revision-checked, so it returns ErrConflict rather than re-describing a row
// another writer advanced after this transaction read it.
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
	// Take the write lock before reading so two verdicts cannot interleave. The
	// placeholder row carries no stage, so even inside this transaction no
	// rolled_back row exists without its evidence; the deferred rollback removes
	// it either way.
	inserted, err := tx.ExecContext(ctx, `insert into `+tableName+`
		(id, stage, created_at_ms, updated_at_ms) values (1, ?, ?, ?)
		on conflict(id) do nothing`, "", now, now)
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
	if !current.Stage.RollbackApplicable() {
		// Stage eligibility is decided here, against the row this transaction
		// holds, not only by the caller's earlier read: a copy-in retry may have
		// parked the row at failed, and preflight never copied a root at all.
		return current, fmt.Errorf("%w: %q -> %q", ErrStageRegression, current.Stage, model.UpgradeStageRolledBack)
	}
	current.Stage = model.UpgradeStageRolledBack
	current.Rollback = &evidence
	current.UpdatedAtMS = nextUpdatedAtMS(now, current)
	if current.CreatedAtMS == 0 {
		current.CreatedAtMS = now
	}
	expectedRevision := current.Revision
	current.Revision = expectedRevision + 1
	// The verdict and the observation this transaction is about to write must
	// both still be acceptable, and a stored clock that ran ahead stays
	// readable; validate the row this transaction persists, not just the
	// caller's evidence, because the clock above is rewritten here.
	if err := current.Validate(); err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("%w: %v", ErrInvalidRow, err)
	}
	// The verdict carries the revision this transaction read, so a row another
	// writer advanced since is reported as ErrConflict instead of being
	// re-described as abandoned.
	result, err := tx.ExecContext(ctx, `update `+tableName+`
		set stage = ?, rollback_image = ?, rollback_image_source = ?, rollback_image_form = ?,
			rollback_status_url = ?, rollback_source_fingerprint = ?, rollback_observed_at_ms = ?,
			revision = ?, created_at_ms = ?, updated_at_ms = ?
		where id = 1 and revision = ?`,
		string(current.Stage), evidence.Image, evidence.ImageSource, evidence.ImageForm,
		evidence.StatusURL, evidence.SourceFingerprint, evidence.ObservedAtMS,
		current.Revision, current.CreatedAtMS, current.UpdatedAtMS, expectedRevision)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if err := revisionGuard(result, expectedRevision); err != nil {
		return model.UpgradeOperation{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.UpgradeOperation{}, err
	}
	return current, nil
}
