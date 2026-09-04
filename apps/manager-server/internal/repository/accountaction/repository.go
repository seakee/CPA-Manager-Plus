package accountaction

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

type Repository interface {
	Upsert(ctx context.Context, input model.AccountActionCandidateUpsert) (model.AccountActionCandidate, error)
	List(ctx context.Context, status string, limit int) ([]model.AccountActionCandidate, error)
	Count(ctx context.Context, status string) (int64, error)
	Get(ctx context.Context, id int64) (model.AccountActionCandidate, bool, error)
	UpdateStatus(ctx context.Context, id int64, status string) (model.AccountActionCandidate, error)
	UpdatePendingStatus(ctx context.Context, id int64, status string) (model.AccountActionCandidate, error)
	RecordFailure(ctx context.Context, id int64, reason string) error
	MarkAutoDisabled(ctx context.Context, id int64, disabledAtMS int64) error
}

type repository struct {
	db *sql.DB
}

func New(db *sql.DB) Repository {
	return &repository{db: db}
}

func (r *repository) Upsert(ctx context.Context, input model.AccountActionCandidateUpsert) (model.AccountActionCandidate, error) {
	input.AuthFileName = strings.TrimSpace(input.AuthFileName)
	if input.AuthFileName == "" {
		return model.AccountActionCandidate{}, errors.New("auth file name is required")
	}
	input.ActionType = normalizeActionType(input.ActionType)
	input.Provider = normalizeProvider(input.Provider)
	input.AuthIndex = strings.TrimSpace(input.AuthIndex)
	input.AccountSnapshot = strings.TrimSpace(input.AccountSnapshot)
	if input.AccountSnapshot == input.AuthFileName {
		input.AccountSnapshot = ""
	}
	input.AccountIDSnapshot = strings.TrimSpace(input.AccountIDSnapshot)
	if input.Provider == "codex" {
		if input.AccountIDSnapshot != "" {
			workspace, ok := usageidentity.NormalizeCodexWorkspaceSnapshot(input.AccountIDSnapshot)
			if !ok {
				return model.AccountActionCandidate{}, errors.New("invalid Codex workspace identity")
			}
			input.AccountIDSnapshot = workspace
		}
		if input.AccountSnapshot != "" {
			if member, ok := usageidentity.NormalizeCodexMemberSnapshot(input.AccountSnapshot); ok {
				input.AccountSnapshot = member
			} else {
				// A display label is not a Codex member identity. It may remain
				// in evidence, but must not participate in candidate ownership.
				input.AccountSnapshot = ""
			}
		}
	}
	input.AuthLabel = strings.TrimSpace(input.AuthLabel)
	input.ReasonCode = strings.TrimSpace(input.ReasonCode)
	input.Reason = strings.TrimSpace(input.Reason)
	input.EvidenceJSON = strings.TrimSpace(input.EvidenceJSON)

	now := time.Now().UnixMilli()
	seenAt := input.SeenAtMS
	if seenAt <= 0 {
		seenAt = now
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return model.AccountActionCandidate{}, err
	}
	defer tx.Rollback()

	id, found, err := findPendingCandidateID(ctx, tx, input)
	if err != nil {
		return model.AccountActionCandidate{}, err
	}
	if !found {
		id, found, err = findUpgradeableCandidateID(ctx, tx, input)
		if err != nil {
			return model.AccountActionCandidate{}, err
		}
	}
	if found && normalizeProvider(input.Provider) == "codex" && input.AuthIndex != "" {
		existing, err := getByID(ctx, tx, id)
		if err != nil {
			return model.AccountActionCandidate{}, err
		}
		if reason := codexCandidateIdentityConflict(existing, input); reason != "" {
			_, err = tx.ExecContext(ctx, `update account_action_candidates set
				last_error = ?, last_seen_at_ms = ?, hit_count = hit_count + 1, updated_at_ms = ?
				where id = ? and status = ?`,
				"Codex identity conflict: "+reason,
				seenAt,
				now,
				id,
				model.AccountActionStatusPending,
			)
			if err != nil {
				return model.AccountActionCandidate{}, err
			}
			item, err := getByID(ctx, tx, id)
			if err != nil {
				return model.AccountActionCandidate{}, err
			}
			if err := tx.Commit(); err != nil {
				return model.AccountActionCandidate{}, err
			}
			return item, fmt.Errorf("account action candidate identity conflict: %s", reason)
		}
	}
	if !found {
		res, execErr := tx.ExecContext(ctx, `insert into account_action_candidates (
			action_type, status, provider, auth_file_name, auth_index, account_snapshot, account_id_snapshot, auth_label,
			reason_code, reason, auto_disable_eligible, evidence_json, first_seen_at_ms, last_seen_at_ms, hit_count, created_at_ms, updated_at_ms
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			input.ActionType,
			model.AccountActionStatusPending,
			nullString(input.Provider),
			input.AuthFileName,
			nullString(input.AuthIndex),
			nullString(input.AccountSnapshot),
			nullString(input.AccountIDSnapshot),
			nullString(input.AuthLabel),
			nullString(input.ReasonCode),
			nullString(input.Reason),
			boolInt(input.AutoDisableEligible),
			nullString(input.EvidenceJSON),
			seenAt,
			seenAt,
			now,
			now,
		)
		if execErr != nil {
			return model.AccountActionCandidate{}, execErr
		}
		id, err = res.LastInsertId()
		if err != nil {
			return model.AccountActionCandidate{}, err
		}
	} else {
		_, err = tx.ExecContext(ctx, `update account_action_candidates set
				provider = coalesce(nullif(?, ''), provider),
				auth_index = coalesce(nullif(?, ''), auth_index),
				account_snapshot = coalesce(nullif(?, ''), account_snapshot),
				account_id_snapshot = coalesce(nullif(?, ''), account_id_snapshot),
			auth_label = coalesce(nullif(?, ''), auth_label),
			reason_code = coalesce(nullif(?, ''), reason_code),
			reason = coalesce(nullif(?, ''), reason),
			auto_disable_eligible = max(auto_disable_eligible, ?),
			evidence_json = coalesce(nullif(?, ''), evidence_json),
			last_error = null,
			last_seen_at_ms = ?,
			hit_count = hit_count + 1,
			updated_at_ms = ?
				where id = ?`, input.Provider, input.AuthIndex, input.AccountSnapshot, input.AccountIDSnapshot, input.AuthLabel, input.ReasonCode, input.Reason, boolInt(input.AutoDisableEligible), input.EvidenceJSON, seenAt, now, id)
		if err != nil {
			return model.AccountActionCandidate{}, err
		}
	}
	item, err := getByID(ctx, tx, id)
	if err != nil {
		return model.AccountActionCandidate{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.AccountActionCandidate{}, err
	}
	return item, nil
}

func findPendingCandidateID(ctx context.Context, tx *sql.Tx, input model.AccountActionCandidateUpsert) (int64, bool, error) {
	base := `select id from account_action_candidates
		where status = ? and auth_file_name = ? and action_type = ?
		and coalesce(trim(reason_code), '') = ?`
	args := []any{
		model.AccountActionStatusPending,
		input.AuthFileName,
		input.ActionType,
		input.ReasonCode,
	}
	provider := normalizeProvider(input.Provider)
	if provider == "codex" {
		if input.AuthIndex != "" {
			query := base + `
				and coalesce(trim(auth_index), '') = ?
				and (coalesce(lower(replace(trim(provider), '_', '-')), '') = '' or coalesce(lower(replace(trim(provider), '_', '-')), '') = 'codex')`
			args = append(args, input.AuthIndex)
			return querySingleCandidateID(ctx, tx, query+` order by id asc limit 2`, args...)
		}
		if input.AccountIDSnapshot == "" || input.AccountSnapshot == "" {
			// Preserve an unowned review row if the caller wants to display it,
			// but never let incomplete Codex evidence select an existing row.
			return 0, false, nil
		}

		return querySingleCandidateID(ctx, tx, base+`
			and coalesce(trim(auth_index), '') = ''
			and coalesce(lower(replace(trim(provider), '_', '-')), '') = 'codex'
			and trim(account_id_snapshot) = ?
			and lower(trim(account_snapshot)) = ?
			order by id asc limit 2`, append(args, input.AccountIDSnapshot, input.AccountSnapshot)...)
	}

	authIndexIdentity, accountIDIdentity, providerIdentity, accountSnapshotIdentity := candidateIdentity(input)
	return querySingleCandidateID(ctx, tx, base+`
		and coalesce(trim(auth_index), '') = ?
		and case when coalesce(trim(auth_index), '') <> '' then '' else coalesce(trim(account_id_snapshot), '') end = ?
		and case when coalesce(trim(auth_index), '') <> '' then ''
			else case coalesce(lower(replace(trim(provider), '_', '-')), '')
				when 'x-ai' then 'xai'
				when 'grok' then 'xai'
				else coalesce(lower(replace(trim(provider), '_', '-')), '')
			end
		end = ?
		and case when coalesce(trim(auth_index), '') <> '' or coalesce(trim(account_id_snapshot), '') <> '' then ''
			else coalesce(trim(account_snapshot), '')
		end = ?
		order by id asc limit 2`, append(args, authIndexIdentity, accountIDIdentity, providerIdentity, accountSnapshotIdentity)...)
}

func candidateIdentity(input model.AccountActionCandidateUpsert) (authIndex string, accountID string, provider string, accountSnapshot string) {
	authIndex = strings.TrimSpace(input.AuthIndex)
	if authIndex != "" {
		return authIndex, "", "", ""
	}
	accountID = strings.TrimSpace(input.AccountIDSnapshot)
	if accountID != "" {
		return "", accountID, normalizeProvider(input.Provider), ""
	}
	return "", "", normalizeProvider(input.Provider), strings.TrimSpace(input.AccountSnapshot)
}

func codexCandidateIdentityConflict(existing model.AccountActionCandidate, input model.AccountActionCandidateUpsert) string {
	if existingProvider := normalizeProvider(existing.Provider); existingProvider != "" && existingProvider != "codex" {
		return "provider changed"
	}
	if existingWorkspace := strings.TrimSpace(existing.AccountIDSnapshot); existingWorkspace != "" && input.AccountIDSnapshot != "" && existingWorkspace != input.AccountIDSnapshot {
		return fmt.Sprintf("workspace changed from %q to %q", existingWorkspace, input.AccountIDSnapshot)
	}
	existingMember, existingMemberOK := usageidentity.NormalizeCodexMemberSnapshot(existing.AccountSnapshot)
	inputMember, inputMemberOK := usageidentity.NormalizeCodexMemberSnapshot(input.AccountSnapshot)
	if existingMemberOK && inputMemberOK && existingMember != inputMember {
		return fmt.Sprintf("member changed from %q to %q", existingMember, inputMember)
	}
	return ""
}

func findUpgradeableCandidateID(ctx context.Context, tx *sql.Tx, input model.AccountActionCandidateUpsert) (int64, bool, error) {
	provider := normalizeProvider(input.Provider)
	accountSnapshot := strings.TrimSpace(input.AccountSnapshot)
	baseArgs := []any{
		model.AccountActionStatusPending,
		input.AuthFileName,
		input.ActionType,
		input.ReasonCode,
	}
	if provider == "codex" {
		// An old no-index row is only upgradeable when it already carries the
		// same Workspace and strong member evidence. account_id alone is a
		// shared Team Workspace and is never sufficient for an upgrade.
		if input.AuthIndex == "" || input.AccountIDSnapshot == "" || accountSnapshot == "" {
			return 0, false, nil
		}
		return querySingleCandidateID(ctx, tx, `select id from account_action_candidates
			where status = ? and auth_file_name = ? and action_type = ? and coalesce(trim(reason_code), '') = ?
				and coalesce(trim(auth_index), '') = '' and coalesce(trim(account_id_snapshot), '') = ?
				and lower(trim(account_snapshot)) = ?
				and coalesce(lower(replace(trim(provider), '_', '-')), '') = 'codex'
			order by id asc limit 2`, append(baseArgs, input.AccountIDSnapshot, accountSnapshot)...)
	}
	if input.AuthIndex != "" && input.AccountIDSnapshot != "" && provider != "" {
		id, found, err := querySingleCandidateID(ctx, tx, `select id from account_action_candidates
			where status = ? and auth_file_name = ? and action_type = ? and coalesce(trim(reason_code), '') = ?
				and coalesce(trim(auth_index), '') = '' and coalesce(trim(account_id_snapshot), '') = ?
				and case coalesce(lower(replace(trim(provider), '_', '-')), '')
					when 'x-ai' then 'xai'
					when 'grok' then 'xai'
					else coalesce(lower(replace(trim(provider), '_', '-')), '')
				end = ?
			order by id asc limit 2`, append(baseArgs, input.AccountIDSnapshot, provider)...)
		if err != nil || found {
			return id, found, err
		}
	}
	if (input.AuthIndex == "" && input.AccountIDSnapshot == "") || provider == "" || accountSnapshot == "" {
		return 0, false, nil
	}
	return querySingleCandidateID(ctx, tx, `select id from account_action_candidates
		where status = ? and auth_file_name = ? and action_type = ? and coalesce(trim(reason_code), '') = ?
			and coalesce(trim(auth_index), '') = '' and coalesce(trim(account_id_snapshot), '') = ''
			and case coalesce(lower(replace(trim(provider), '_', '-')), '')
				when 'x-ai' then 'xai'
				when 'grok' then 'xai'
				else coalesce(lower(replace(trim(provider), '_', '-')), '')
			end = ?
			and coalesce(trim(account_snapshot), '') = ?
		order by id asc limit 2`, append(baseArgs, provider, accountSnapshot)...)
}

func querySingleCandidateID(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, bool, error) {
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
		return 0, false, fmt.Errorf("account action identity is ambiguous across candidates %v", ids)
	}
	return ids[0], true, nil
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

func (r *repository) List(ctx context.Context, status string, limit int) ([]model.AccountActionCandidate, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	status = strings.TrimSpace(status)
	query := selectCandidates
	args := []any{}
	if status != "" {
		query += ` where status = ?`
		args = append(args, status)
	}
	query += ` order by case status when 'pending' then 0 else 1 end, last_seen_at_ms desc, id desc limit ?`
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.AccountActionCandidate, 0)
	for rows.Next() {
		item, err := scanCandidate(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *repository) Count(ctx context.Context, status string) (int64, error) {
	var count int64
	status = strings.TrimSpace(status)
	if status == "" {
		if err := r.db.QueryRowContext(ctx, `select count(*) from account_action_candidates`).Scan(&count); err != nil {
			return 0, err
		}
		return count, nil
	}
	if err := r.db.QueryRowContext(ctx, `select count(*) from account_action_candidates where status = ?`, status).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (r *repository) Get(ctx context.Context, id int64) (model.AccountActionCandidate, bool, error) {
	if id <= 0 {
		return model.AccountActionCandidate{}, false, nil
	}
	item, err := getByID(ctx, r.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return model.AccountActionCandidate{}, false, nil
	}
	if err != nil {
		return model.AccountActionCandidate{}, false, err
	}
	return item, true, nil
}

func (r *repository) UpdateStatus(ctx context.Context, id int64, status string) (model.AccountActionCandidate, error) {
	return r.updateStatus(ctx, id, status, false)
}

func (r *repository) UpdatePendingStatus(ctx context.Context, id int64, status string) (model.AccountActionCandidate, error) {
	return r.updateStatus(ctx, id, status, true)
}

func (r *repository) updateStatus(ctx context.Context, id int64, status string, pendingOnly bool) (model.AccountActionCandidate, error) {
	status = normalizeStatus(status)
	if id <= 0 {
		return model.AccountActionCandidate{}, errors.New("candidate id is required")
	}
	now := time.Now().UnixMilli()
	query := `update account_action_candidates set status = ?, last_error = null, updated_at_ms = ? where id = ?`
	args := []any{status, now, id}
	if pendingOnly {
		query += ` and status = ?`
		args = append(args, model.AccountActionStatusPending)
	}
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return model.AccountActionCandidate{}, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return model.AccountActionCandidate{}, sql.ErrNoRows
	}
	return r.mustGet(ctx, id)
}

func (r *repository) RecordFailure(ctx context.Context, id int64, reason string) error {
	if id <= 0 {
		return errors.New("candidate id is required")
	}
	_, err := r.db.ExecContext(ctx, `update account_action_candidates set last_error = ?, updated_at_ms = ? where id = ?`, nullString(reason), time.Now().UnixMilli(), id)
	return err
}

func (r *repository) MarkAutoDisabled(ctx context.Context, id int64, disabledAtMS int64) error {
	if id <= 0 {
		return errors.New("candidate id is required")
	}
	if disabledAtMS <= 0 {
		disabledAtMS = time.Now().UnixMilli()
	}
	res, err := r.db.ExecContext(ctx, `update account_action_candidates set auto_disabled_at_ms = ?, last_error = null, updated_at_ms = ? where id = ? and status = ?`, disabledAtMS, disabledAtMS, id, model.AccountActionStatusPending)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *repository) mustGet(ctx context.Context, id int64) (model.AccountActionCandidate, error) {
	item, ok, err := r.Get(ctx, id)
	if err != nil {
		return model.AccountActionCandidate{}, err
	}
	if !ok {
		return model.AccountActionCandidate{}, sql.ErrNoRows
	}
	return item, nil
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const selectCandidates = `select id, action_type, status, provider, auth_file_name, auth_index, account_snapshot, account_id_snapshot, auth_label,
	reason_code, reason, auto_disable_eligible, auto_disabled_at_ms, evidence_json, last_error, first_seen_at_ms, last_seen_at_ms, hit_count, created_at_ms, updated_at_ms
	from account_action_candidates`

func getByID(ctx context.Context, q queryer, id int64) (model.AccountActionCandidate, error) {
	return scanCandidate(q.QueryRowContext(ctx, selectCandidates+` where id = ?`, id))
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCandidate(row rowScanner) (model.AccountActionCandidate, error) {
	var item model.AccountActionCandidate
	var provider, authIndex, accountSnapshot, accountIDSnapshot, authLabel, reasonCode, reason, evidenceJSON, lastError sql.NullString
	var autoDisableEligible int
	var autoDisabledAtMS sql.NullInt64
	if err := row.Scan(
		&item.ID,
		&item.ActionType,
		&item.Status,
		&provider,
		&item.AuthFileName,
		&authIndex,
		&accountSnapshot,
		&accountIDSnapshot,
		&authLabel,
		&reasonCode,
		&reason,
		&autoDisableEligible,
		&autoDisabledAtMS,
		&evidenceJSON,
		&lastError,
		&item.FirstSeenAtMS,
		&item.LastSeenAtMS,
		&item.HitCount,
		&item.CreatedAtMS,
		&item.UpdatedAtMS,
	); err != nil {
		return model.AccountActionCandidate{}, err
	}
	item.Provider = provider.String
	item.AuthIndex = authIndex.String
	item.AccountSnapshot = accountSnapshot.String
	item.AccountIDSnapshot = accountIDSnapshot.String
	item.AuthLabel = authLabel.String
	item.ReasonCode = reasonCode.String
	item.Reason = reason.String
	item.AutoDisableEligible = autoDisableEligible != 0
	if autoDisabledAtMS.Valid {
		item.AutoDisabledAtMS = autoDisabledAtMS.Int64
	}
	item.EvidenceJSON = evidenceJSON.String
	item.LastError = lastError.String
	if item.EvidenceJSON != "" {
		var evidence any
		if err := json.Unmarshal([]byte(item.EvidenceJSON), &evidence); err == nil {
			item.Evidence = evidence
		}
	}
	return item, nil
}

func normalizeActionType(value string) string {
	switch strings.TrimSpace(value) {
	case model.AccountActionTypeReauth:
		return model.AccountActionTypeReauth
	case model.AccountActionTypeReview:
		return model.AccountActionTypeReview
	default:
		return model.AccountActionTypeDelete
	}
}

func normalizeStatus(value string) string {
	switch strings.TrimSpace(value) {
	case model.AccountActionStatusIgnored:
		return model.AccountActionStatusIgnored
	case model.AccountActionStatusResolved:
		return model.AccountActionStatusResolved
	case model.AccountActionStatusDeleted:
		return model.AccountActionStatusDeleted
	default:
		return model.AccountActionStatusPending
	}
}

func nullString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
