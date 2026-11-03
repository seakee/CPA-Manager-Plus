package quotacooldown

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

type Repository interface {
	UpsertActive(ctx context.Context, cooldown model.QuotaCooldownUpsert) (model.QuotaCooldown, error)
	ListDue(ctx context.Context, nowMS int64, limit int) ([]model.QuotaCooldown, error)
	ListActive(ctx context.Context) ([]model.QuotaCooldown, error)
	MarkRecovered(ctx context.Context, id int64, expectedUpdatedAtMS int64, recoveredAtMS int64) error
	// ClaimRecovery retires an observed active generation before CPAMP mutates
	// remote credential state and returns the claimed generation.
	ClaimRecovery(ctx context.Context, id int64, expectedUpdatedAtMS int64, reason string) (int64, error)
	// MarkClaimedRecovered finalizes a claimed generation as recovered.
	MarkClaimedRecovered(ctx context.Context, id int64, claimedUpdatedAtMS int64, recoveredAtMS int64) error
	// MarkClaimedSkipped records why a claimed generation will not be confirmed.
	MarkClaimedSkipped(ctx context.Context, id int64, claimedUpdatedAtMS int64, reason string) error
	// ReopenClaim returns a claimed generation to active so it can retry.
	ReopenClaim(ctx context.Context, id int64, claimedUpdatedAtMS int64, reason string) error
	MarkSkipped(ctx context.Context, id int64, expectedUpdatedAtMS int64, reason string) error
	RecordFailure(ctx context.Context, id int64, reason string) error
}

// ErrConflict reports that the cooldown row no longer matches the generation a
// recovery decision was made for. Another writer advanced it, so the caller
// must not treat its own enable decision as the current truth.
var ErrConflict = errors.New("quota cooldown generation conflict")

// ErrSuperseded reports that a claimed generation cannot be reopened because
// another active cooldown already owns the same credential. Ownership follows
// UpsertActive, so a legacy row without an auth_index and the indexed row for
// the same provider and account snapshot are one owner. The claimed row stays
// skipped and the active owner keeps the authority. It wraps ErrConflict so
// callers that only distinguish stale generations keep their existing behavior.
var ErrSuperseded = fmt.Errorf("%w: a newer active cooldown owns the same credential identity", ErrConflict)

type repository struct {
	db *sql.DB
}

func New(db *sql.DB) Repository {
	return &repository{db: db}
}

func (r *repository) UpsertActive(ctx context.Context, cooldown model.QuotaCooldownUpsert) (model.QuotaCooldown, error) {
	cooldown.AuthFileName = strings.TrimSpace(cooldown.AuthFileName)
	cooldown.AuthIndex = strings.TrimSpace(cooldown.AuthIndex)
	cooldown.AccountSnapshot = strings.TrimSpace(cooldown.AccountSnapshot)
	if cooldown.AccountSnapshot == cooldown.AuthFileName {
		cooldown.AccountSnapshot = ""
	}
	cooldown.Provider = normalizeProvider(cooldown.Provider)
	cooldown.Owner = strings.TrimSpace(cooldown.Owner)
	if cooldown.AuthFileName == "" {
		return model.QuotaCooldown{}, errors.New("quota cooldown auth file name is required")
	}
	if cooldown.Owner == "" {
		return model.QuotaCooldown{}, errors.New("quota cooldown owner is required")
	}
	if cooldown.RecoverAtMS <= 0 {
		return model.QuotaCooldown{}, errors.New("quota cooldown recover_at_ms is required")
	}
	cooldown.EvidenceJSON = normalizeEvidenceJSON(cooldown.EvidenceJSON)
	now := time.Now().UnixMilli()
	if cooldown.DisabledAtMS <= 0 {
		cooldown.DisabledAtMS = now
	}
	if cooldown.ObservedEnabledAtMS > cooldown.DisabledAtMS {
		return model.QuotaCooldown{}, errors.New("quota cooldown observed_enabled_at_ms must not exceed disabled_at_ms")
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return model.QuotaCooldown{}, err
	}
	defer tx.Rollback()

	authIndexIdentity, providerIdentity, accountSnapshotIdentity := cooldownIdentity(cooldown)
	id, found, err := querySingleCooldownID(ctx, tx, `select id from quota_cooldowns
		where auth_file_name = ? and owner = ? and status = ?
			and `+cooldownIdentityAuthIndexSQL+` = ?
			and `+cooldownIdentityProviderSQL+` = ?
			and `+cooldownIdentityAccountSnapshotSQL+` = ?
		order by id asc limit 2`,
		cooldown.AuthFileName,
		cooldown.Owner,
		model.QuotaCooldownStatusActive,
		authIndexIdentity,
		providerIdentity,
		accountSnapshotIdentity,
	)
	if err != nil {
		return model.QuotaCooldown{}, err
	}
	if !found && authIndexIdentity != "" {
		fallbackProvider := normalizeProvider(cooldown.Provider)
		fallbackSnapshot := strings.TrimSpace(cooldown.AccountSnapshot)
		if fallbackProvider != "" && fallbackSnapshot != "" {
			id, found, err = querySingleCooldownID(ctx, tx, `select id from quota_cooldowns
				where auth_file_name = ? and owner = ? and status = ?
					and `+cooldownIdentityAuthIndexSQL+` = ''
					and `+cooldownStoredProviderSQL+` = ?
					and `+cooldownStoredAccountSnapshotSQL+` = ?
				order by id asc limit 2`,
				cooldown.AuthFileName,
				cooldown.Owner,
				model.QuotaCooldownStatusActive,
				fallbackProvider,
				fallbackSnapshot,
			)
			if err != nil {
				return model.QuotaCooldown{}, err
			}
		}
	}
	if found && cooldown.ObservedEnabledAtMS > 0 {
		_, err = tx.ExecContext(ctx, `update quota_cooldowns set
			status = ?,
			recovered_at_ms = ?,
			last_error = null,
			updated_at_ms = max(updated_at_ms + 1, ?)
			where id = ? and status = ?`,
			model.QuotaCooldownStatusRecovered,
			cooldown.ObservedEnabledAtMS,
			now,
			id,
			model.QuotaCooldownStatusActive,
		)
		if err != nil {
			return model.QuotaCooldown{}, err
		}
		found = false
	}
	if !found {
		res, execErr := tx.ExecContext(ctx, `insert into quota_cooldowns (
			auth_file_name, auth_index, account_snapshot, provider, reason_code, window_kind, evidence_json, recover_at_ms,
			owner, event_hash, pre_disabled_state, status, disabled_at_ms,
			created_at_ms, updated_at_ms
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			cooldown.AuthFileName,
			nullString(cooldown.AuthIndex),
			nullString(cooldown.AccountSnapshot),
			nullString(cooldown.Provider),
			nullString(cooldown.ReasonCode),
			nullString(cooldown.WindowKind),
			nullString(cooldown.EvidenceJSON),
			cooldown.RecoverAtMS,
			cooldown.Owner,
			nullString(cooldown.EventHash),
			boolInt(cooldown.PreDisabledState),
			model.QuotaCooldownStatusActive,
			cooldown.DisabledAtMS,
			now,
			now,
		)
		if execErr != nil {
			return model.QuotaCooldown{}, execErr
		}
		id, err = res.LastInsertId()
		if err != nil {
			return model.QuotaCooldown{}, err
		}
	} else {
		_, err = tx.ExecContext(ctx, `update quota_cooldowns set
			auth_index = ?,
			account_snapshot = ?,
			provider = ?,
			reason_code = case
				when ? >= recover_at_ms then coalesce(nullif(?, ''), reason_code)
				else reason_code
			end,
			window_kind = case
				when ? >= recover_at_ms then coalesce(nullif(?, ''), window_kind)
				else window_kind
			end,
			evidence_json = case
				when ? >= recover_at_ms then coalesce(nullif(?, ''), evidence_json)
				else evidence_json
			end,
			recover_at_ms = max(recover_at_ms, ?),
			event_hash = case
				when ? >= recover_at_ms then coalesce(nullif(?, ''), event_hash)
				else event_hash
			end,
			disabled_at_ms = min(disabled_at_ms, ?),
			last_error = null,
			updated_at_ms = max(updated_at_ms + 1, ?)
		where id = ?`,
			nullString(cooldown.AuthIndex),
			nullString(cooldown.AccountSnapshot),
			nullString(cooldown.Provider),
			cooldown.RecoverAtMS,
			cooldown.ReasonCode,
			cooldown.RecoverAtMS,
			cooldown.WindowKind,
			cooldown.RecoverAtMS,
			cooldown.EvidenceJSON,
			cooldown.RecoverAtMS,
			cooldown.RecoverAtMS,
			cooldown.EventHash,
			cooldown.DisabledAtMS,
			now,
			id,
		)
		if err != nil {
			return model.QuotaCooldown{}, err
		}
	}
	item, ok, err := getByID(ctx, tx, id)
	if err != nil {
		return model.QuotaCooldown{}, err
	}
	if !ok {
		return model.QuotaCooldown{}, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return model.QuotaCooldown{}, err
	}
	return item, nil
}

func querySingleCooldownID(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, bool, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	ids := make([]int64, 0, 2)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, false, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, false, err
	}
	if len(ids) == 0 {
		return 0, false, nil
	}
	if len(ids) > 1 {
		return 0, false, fmt.Errorf("quota cooldown identity is ambiguous across records %v", ids)
	}
	return ids[0], true, nil
}

func cooldownIdentity(cooldown model.QuotaCooldownUpsert) (authIndex string, provider string, accountSnapshot string) {
	authIndex = strings.TrimSpace(cooldown.AuthIndex)
	if authIndex != "" {
		return authIndex, "", ""
	}
	return "", normalizeProvider(cooldown.Provider), strings.TrimSpace(cooldown.AccountSnapshot)
}

func normalizeProvider(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	switch normalized {
	case "x-ai", "grok":
		return "xai"
	default:
		return normalized
	}
}

func cooldownColumn(table string, name string) string {
	return table + "." + name
}

// cooldownAuthIndexColumn returns the trimmed auth index of a cooldown row and
// normalizes NULL to the empty legacy index.
func cooldownAuthIndexColumn(table string) string {
	return `coalesce(trim(` + cooldownColumn(table, "auth_index") + `), '')`
}

// cooldownNormalizedProviderColumn returns the provider of a cooldown row in
// the form normalizeProvider produces.
func cooldownNormalizedProviderColumn(table string) string {
	normalized := `coalesce(lower(replace(trim(` + cooldownColumn(table, "provider") + `), '_', '-')), '')`
	return `case ` + normalized + `
			when 'x-ai' then 'xai'
			when 'grok' then 'xai'
			else ` + normalized + `
		end`
}

// cooldownAccountSnapshotColumn returns the trimmed account snapshot of a
// cooldown row and normalizes NULL to empty.
func cooldownAccountSnapshotColumn(table string) string {
	return `coalesce(trim(` + cooldownColumn(table, "account_snapshot") + `), '')`
}

// cooldownIdentityColumns returns the SQL expressions that define a cooldown
// identity for the given table reference. UpsertActive resolves an active row
// by these expressions and ReopenClaim refuses to reactivate a claimed row
// while another active row matches them, so both call sites share this single
// definition and cannot drift apart. They mirror cooldownIdentity.
func cooldownIdentityColumns(table string) (authIndex string, provider string, accountSnapshot string) {
	authIndex = cooldownAuthIndexColumn(table)
	provider = `case when ` + authIndex + ` <> '' then '' else ` + cooldownNormalizedProviderColumn(table) + ` end`
	accountSnapshot = `case when ` + authIndex + ` <> '' then '' else ` + cooldownAccountSnapshotColumn(table) + ` end`
	return authIndex, provider, accountSnapshot
}

// cooldownSameCredentialSQL returns the predicate that decides whether two
// cooldown rows address the same physical credential. Callers restrict both
// rows to the same auth_file_name and owner already.
//
// A row carrying an auth_index owns exactly that index; a row without one owns
// its normalized provider and account snapshot. The second branch is the legacy
// fallback UpsertActive applies to an indexed request that finds no exact
// active row: it merges into the empty-index row with the same provider and
// account snapshot. Comparing both identified shapes here keeps the reopen
// guard and UpsertActive from disagreeing about which rows are one credential.
func cooldownSameCredentialSQL(left string, right string) string {
	leftAuthIndex := cooldownAuthIndexColumn(left)
	rightAuthIndex := cooldownAuthIndexColumn(right)
	leftProvider := cooldownNormalizedProviderColumn(left)
	rightProvider := cooldownNormalizedProviderColumn(right)
	leftAccountSnapshot := cooldownAccountSnapshotColumn(left)
	rightAccountSnapshot := cooldownAccountSnapshotColumn(right)
	_, leftIdentityProvider, leftIdentityAccountSnapshot := cooldownIdentityColumns(left)
	_, rightIdentityProvider, rightIdentityAccountSnapshot := cooldownIdentityColumns(right)
	exact := `(` + leftAuthIndex + ` = ` + rightAuthIndex +
		` and ` + leftIdentityProvider + ` = ` + rightIdentityProvider +
		` and ` + leftIdentityAccountSnapshot + ` = ` + rightIdentityAccountSnapshot + `)`
	legacyFallback := `((` + leftAuthIndex + ` = '' and ` + rightAuthIndex + ` <> '') or (` +
		leftAuthIndex + ` <> '' and ` + rightAuthIndex + ` = ''))` +
		` and ` + leftProvider + ` <> '' and ` + leftProvider + ` = ` + rightProvider +
		` and ` + leftAccountSnapshot + ` <> '' and ` + leftAccountSnapshot + ` = ` + rightAccountSnapshot
	return `(` + exact + ` or (` + legacyFallback + `))`
}

var (
	cooldownIdentityAuthIndexSQL, cooldownIdentityProviderSQL, cooldownIdentityAccountSnapshotSQL = cooldownIdentityColumns("quota_cooldowns")
	// The provider and account snapshot as stored, without the identity
	// projection that blanks them for indexed rows. UpsertActive's legacy
	// fallback lookup matches an incoming indexed cooldown against these.
	cooldownStoredProviderSQL        = cooldownNormalizedProviderColumn("quota_cooldowns")
	cooldownStoredAccountSnapshotSQL = cooldownAccountSnapshotColumn("quota_cooldowns")
)

func (r *repository) ListDue(ctx context.Context, nowMS int64, limit int) ([]model.QuotaCooldown, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, selectQuotaCooldowns+` where status = ? and recover_at_ms <= ? order by recover_at_ms asc, id asc limit ?`, model.QuotaCooldownStatusActive, nowMS, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanList(rows)
}

func (r *repository) ListActive(ctx context.Context) ([]model.QuotaCooldown, error) {
	rows, err := r.db.QueryContext(ctx, selectQuotaCooldowns+` where status = ? order by recover_at_ms asc, id asc`, model.QuotaCooldownStatusActive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanList(rows)
}

func (r *repository) MarkRecovered(ctx context.Context, id int64, expectedUpdatedAtMS int64, recoveredAtMS int64) error {
	if recoveredAtMS <= 0 {
		recoveredAtMS = time.Now().UnixMilli()
	}
	if expectedUpdatedAtMS <= 0 {
		return fmt.Errorf("%w: expected generation is required", ErrConflict)
	}
	result, err := r.db.ExecContext(
		ctx,
		`update quota_cooldowns set status = ?, recovered_at_ms = ?, last_error = null, updated_at_ms = ? where id = ? and status = ? and updated_at_ms = ?`,
		model.QuotaCooldownStatusRecovered, recoveredAtMS, recoveredAtMS, id, model.QuotaCooldownStatusActive, expectedUpdatedAtMS,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrConflict
	}
	return nil
}

func (r *repository) MarkSkipped(ctx context.Context, id int64, expectedUpdatedAtMS int64, reason string) error {
	if expectedUpdatedAtMS <= 0 {
		return fmt.Errorf("%w: expected generation is required", ErrConflict)
	}
	return r.casGeneration(ctx, `update quota_cooldowns set status = ?, last_error = ?, updated_at_ms = ? where id = ? and status = ? and updated_at_ms = ?`,
		model.QuotaCooldownStatusSkipped, nullString(reason), nextUpdatedAtMS(expectedUpdatedAtMS), id, model.QuotaCooldownStatusActive, expectedUpdatedAtMS,
	)
}

// ClaimRecovery retires an observed active generation before CPAMP mutates the
// remote credential. The row becomes terminal, so a restart, a later due tick,
// or a retry with the same generation can never re-use it to enable a
// credential that a human disabled after the request left the process.
func (r *repository) ClaimRecovery(ctx context.Context, id int64, expectedUpdatedAtMS int64, reason string) (int64, error) {
	if expectedUpdatedAtMS <= 0 {
		return 0, fmt.Errorf("%w: expected generation is required", ErrConflict)
	}
	claimedUpdatedAtMS := nextUpdatedAtMS(expectedUpdatedAtMS)
	if err := r.casGeneration(ctx, `update quota_cooldowns set status = ?, last_error = ?, updated_at_ms = ? where id = ? and status = ? and updated_at_ms = ?`,
		model.QuotaCooldownStatusSkipped, nullString(reason), claimedUpdatedAtMS, id, model.QuotaCooldownStatusActive, expectedUpdatedAtMS,
	); err != nil {
		return 0, err
	}
	return claimedUpdatedAtMS, nil
}

func (r *repository) MarkClaimedRecovered(ctx context.Context, id int64, claimedUpdatedAtMS int64, recoveredAtMS int64) error {
	if claimedUpdatedAtMS <= 0 {
		return fmt.Errorf("%w: claimed generation is required", ErrConflict)
	}
	if recoveredAtMS <= 0 {
		recoveredAtMS = time.Now().UnixMilli()
	}
	return r.casGeneration(ctx, `update quota_cooldowns set status = ?, recovered_at_ms = ?, last_error = null, updated_at_ms = ? where id = ? and status = ? and updated_at_ms = ?`,
		model.QuotaCooldownStatusRecovered, recoveredAtMS, nextUpdatedAtMS(claimedUpdatedAtMS), id, model.QuotaCooldownStatusSkipped, claimedUpdatedAtMS,
	)
}

func (r *repository) MarkClaimedSkipped(ctx context.Context, id int64, claimedUpdatedAtMS int64, reason string) error {
	if claimedUpdatedAtMS <= 0 {
		return fmt.Errorf("%w: claimed generation is required", ErrConflict)
	}
	return r.casGeneration(ctx, `update quota_cooldowns set last_error = ?, updated_at_ms = ? where id = ? and status = ? and updated_at_ms = ?`,
		nullString(reason), nextUpdatedAtMS(claimedUpdatedAtMS), id, model.QuotaCooldownStatusSkipped, claimedUpdatedAtMS,
	)
}

func (r *repository) ReopenClaim(ctx context.Context, id int64, claimedUpdatedAtMS int64, reason string) error {
	if claimedUpdatedAtMS <= 0 {
		return fmt.Errorf("%w: claimed generation is required", ErrConflict)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Reactivate the claimed row only while it is still at the claimed
	// generation and no other active row already owns the same credential.
	// cooldownSameCredentialSQL resolves that ownership exactly like
	// UpsertActive does, including the legacy empty-index fallback, so a newer
	// active generation can never be joined by a second active owner for the
	// same credential.
	result, err := tx.ExecContext(ctx, `update quota_cooldowns set status = ?, last_error = ?, updated_at_ms = ?
		where id = ? and status = ? and updated_at_ms = ?
			and not exists (
				select 1 from quota_cooldowns as sibling
				where sibling.id <> quota_cooldowns.id
					and sibling.auth_file_name = quota_cooldowns.auth_file_name
					and sibling.owner = quota_cooldowns.owner
					and sibling.status = ?
					and `+cooldownSameCredentialSQL("sibling", "quota_cooldowns")+`
			)`,
		model.QuotaCooldownStatusActive, nullString(reason), nextUpdatedAtMS(claimedUpdatedAtMS),
		id, model.QuotaCooldownStatusSkipped, claimedUpdatedAtMS,
		model.QuotaCooldownStatusActive,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return tx.Commit()
	}
	superseded, err := claimedGenerationSuperseded(ctx, tx, id, claimedUpdatedAtMS)
	if err != nil {
		return err
	}
	if superseded {
		return ErrSuperseded
	}
	return ErrConflict
}

// claimedGenerationSuperseded reports whether the claimed row is still at the
// generation the caller observed while another active row already owns the
// same credential. Ownership is resolved by cooldownSameCredentialSQL, so a
// claimed legacy row and an indexed row for the same provider and account
// snapshot count as one owner. It distinguishes a reopen that lost the
// credential to a newer active generation from a generation that moved on its
// own.
func claimedGenerationSuperseded(ctx context.Context, tx *sql.Tx, id int64, claimedUpdatedAtMS int64) (bool, error) {
	var superseded int
	err := tx.QueryRowContext(ctx, `select exists (
		select 1 from quota_cooldowns as claimed
		join quota_cooldowns as sibling on
			sibling.id <> claimed.id
			and sibling.auth_file_name = claimed.auth_file_name
			and sibling.owner = claimed.owner
			and sibling.status = ?
			and `+cooldownSameCredentialSQL("sibling", "claimed")+`
		where claimed.id = ? and claimed.status = ? and claimed.updated_at_ms = ?
	)`,
		model.QuotaCooldownStatusActive,
		id, model.QuotaCooldownStatusSkipped, claimedUpdatedAtMS,
	).Scan(&superseded)
	if err != nil {
		return false, err
	}
	return superseded != 0, nil
}

// casGeneration applies a generation-bound transition and reports ErrConflict
// when the row no longer matches the generation the decision was made for. A
// conflicting generation is never modified.
func (r *repository) casGeneration(ctx context.Context, query string, args ...any) error {
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrConflict
	}
	return nil
}

// nextUpdatedAtMS advances a generation strictly past the generation it
// replaces, even within the same millisecond or after a clock rollback.
func nextUpdatedAtMS(expectedUpdatedAtMS int64) int64 {
	if now := time.Now().UnixMilli(); now > expectedUpdatedAtMS {
		return now
	}
	return expectedUpdatedAtMS + 1
}

func (r *repository) RecordFailure(ctx context.Context, id int64, reason string) error {
	now := time.Now().UnixMilli()
	_, err := r.db.ExecContext(ctx, `update quota_cooldowns set last_error = ?, updated_at_ms = max(updated_at_ms + 1, ?) where id = ? and status = ?`, nullString(reason), now, id, model.QuotaCooldownStatusActive)
	return err
}

const selectQuotaCooldowns = `select
	id, auth_file_name, auth_index, account_snapshot, provider, reason_code, window_kind, evidence_json, recover_at_ms,
	owner, event_hash, pre_disabled_state, status, disabled_at_ms,
	recovered_at_ms, last_error, created_at_ms, updated_at_ms
from quota_cooldowns`

func getByID(ctx context.Context, q queryer, id int64) (model.QuotaCooldown, bool, error) {
	row := q.QueryRowContext(ctx, selectQuotaCooldowns+` where id = ?`, id)
	item, err := scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.QuotaCooldown{}, false, nil
	}
	if err != nil {
		return model.QuotaCooldown{}, false, err
	}
	return item, true, nil
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func scanList(rows *sql.Rows) ([]model.QuotaCooldown, error) {
	items := make([]model.QuotaCooldown, 0)
	for rows.Next() {
		item, err := scanScanner(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func scanRow(row *sql.Row) (model.QuotaCooldown, error) {
	return scanScanner(row)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanScanner(row scanner) (model.QuotaCooldown, error) {
	var item model.QuotaCooldown
	var authIndex sql.NullString
	var accountSnapshot sql.NullString
	var provider sql.NullString
	var reasonCode, windowKind, evidenceJSON, eventHash sql.NullString
	var recoveredAtMS sql.NullInt64
	var lastError sql.NullString
	var preDisabled int
	err := row.Scan(
		&item.ID,
		&item.AuthFileName,
		&authIndex,
		&accountSnapshot,
		&provider,
		&reasonCode,
		&windowKind,
		&evidenceJSON,
		&item.RecoverAtMS,
		&item.Owner,
		&eventHash,
		&preDisabled,
		&item.Status,
		&item.DisabledAtMS,
		&recoveredAtMS,
		&lastError,
		&item.CreatedAtMS,
		&item.UpdatedAtMS,
	)
	if err != nil {
		return model.QuotaCooldown{}, err
	}
	item.AuthIndex = authIndex.String
	item.AccountSnapshot = accountSnapshot.String
	item.Provider = provider.String
	item.ReasonCode = reasonCode.String
	item.WindowKind = windowKind.String
	item.EvidenceJSON = evidenceJSON.String
	item.EventHash = eventHash.String
	item.PreDisabledState = preDisabled != 0
	if recoveredAtMS.Valid {
		item.RecoveredAtMS = recoveredAtMS.Int64
	}
	item.LastError = lastError.String
	return item, nil
}

func nullString(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func normalizeEvidenceJSON(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !json.Valid([]byte(value)) {
		return ""
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
