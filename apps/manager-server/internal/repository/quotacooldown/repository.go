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
	MarkRecovered(ctx context.Context, id int64, recoveredAtMS int64) error
	MarkSkipped(ctx context.Context, id int64, reason string) error
	RecordFailure(ctx context.Context, id int64, reason string) error
}

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

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return model.QuotaCooldown{}, err
	}
	defer tx.Rollback()

	authIndexIdentity, providerIdentity, accountSnapshotIdentity := cooldownIdentity(cooldown)
	id, found, err := querySingleCooldownID(ctx, tx, `select id from quota_cooldowns
		where auth_file_name = ? and owner = ? and status = ?
			and coalesce(trim(auth_index), '') = ?
			and case
				when coalesce(trim(auth_index), '') <> '' then ''
				else case coalesce(lower(replace(trim(provider), '_', '-')), '')
					when 'x-ai' then 'xai'
					when 'grok' then 'xai'
					else coalesce(lower(replace(trim(provider), '_', '-')), '')
				end
			end = ?
			and case
				when coalesce(trim(auth_index), '') <> '' then ''
				else coalesce(trim(account_snapshot), '')
			end = ?
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
					and coalesce(trim(auth_index), '') = ''
					and case coalesce(lower(replace(trim(provider), '_', '-')), '')
						when 'x-ai' then 'xai'
						when 'grok' then 'xai'
						else coalesce(lower(replace(trim(provider), '_', '-')), '')
					end = ?
					and coalesce(trim(account_snapshot), '') = ?
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
			updated_at_ms = ?
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

func (r *repository) MarkRecovered(ctx context.Context, id int64, recoveredAtMS int64) error {
	if recoveredAtMS <= 0 {
		recoveredAtMS = time.Now().UnixMilli()
	}
	_, err := r.db.ExecContext(ctx, `update quota_cooldowns set status = ?, recovered_at_ms = ?, last_error = null, updated_at_ms = ? where id = ?`, model.QuotaCooldownStatusRecovered, recoveredAtMS, recoveredAtMS, id)
	return err
}

func (r *repository) MarkSkipped(ctx context.Context, id int64, reason string) error {
	now := time.Now().UnixMilli()
	_, err := r.db.ExecContext(ctx, `update quota_cooldowns set status = ?, last_error = ?, updated_at_ms = ? where id = ?`, model.QuotaCooldownStatusSkipped, nullString(reason), now, id)
	return err
}

func (r *repository) RecordFailure(ctx context.Context, id int64, reason string) error {
	now := time.Now().UnixMilli()
	_, err := r.db.ExecContext(ctx, `update quota_cooldowns set last_error = ?, updated_at_ms = ? where id = ? and status = ?`, nullString(reason), now, id, model.QuotaCooldownStatusActive)
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
