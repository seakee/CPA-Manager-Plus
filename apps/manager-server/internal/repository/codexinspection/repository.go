package codexinspection

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

type Repository interface {
	CreateRun(ctx context.Context, run model.CodexInspectionRun) (model.CodexInspectionRun, error)
	UpdateRun(ctx context.Context, run model.CodexInspectionRun) error
	InsertResult(ctx context.Context, result model.CodexInspectionResult) (model.CodexInspectionResult, error)
	InsertLog(ctx context.Context, entry model.CodexInspectionLog) (model.CodexInspectionLog, error)
	ListRuns(ctx context.Context, limit int) ([]model.CodexInspectionRun, error)
	GetRun(ctx context.Context, id int64) (model.CodexInspectionRun, bool, error)
	GetLatestRunByTrigger(ctx context.Context, triggerType, triggerKey string) (model.CodexInspectionRun, bool, error)
	ListResults(ctx context.Context, runID int64) ([]model.CodexInspectionResult, error)
	ListLogs(ctx context.Context, runID int64) ([]model.CodexInspectionLog, error)
	ListDisableOwnership(ctx context.Context) ([]model.CodexInspectionDisableOwnership, error)
	UpsertDisableOwnership(ctx context.Context, item model.CodexInspectionDisableOwnership) error
	DeleteDisableOwnership(ctx context.Context, fileName string) error
	RevokeDisableOwnership(ctx context.Context, fileNames []string, clearAll bool) ([]model.CodexInspectionDisableOwnership, error)
	RestoreDisableOwnership(ctx context.Context, items []model.CodexInspectionDisableOwnership) error
}

type repository struct {
	db *sql.DB
}

func New(db *sql.DB) Repository {
	return &repository{db: db}
}

func (r *repository) CreateRun(ctx context.Context, run model.CodexInspectionRun) (model.CodexInspectionRun, error) {
	now := time.Now().UnixMilli()
	if run.StartedAtMS <= 0 {
		run.StartedAtMS = now
	}
	if run.CreatedAtMS <= 0 {
		run.CreatedAtMS = now
	}
	run.UpdatedAtMS = now
	if run.Status == "" {
		run.Status = model.CodexInspectionStatusRunning
	}
	if run.SettingsJSON == "" {
		run.SettingsJSON = model.MarshalCodexInspectionSettings(run.Settings)
	}
	res, err := r.db.ExecContext(
		ctx,
		`insert into codex_inspection_runs (
			trigger_type, trigger_key, status, started_at_ms, finished_at_ms,
			total_files, probe_set_count, sampled_count, disabled_count, enabled_count,
			delete_count, disable_count, enable_count, reauth_count, keep_count, error,
			settings_json, created_at_ms, updated_at_ms
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.TriggerType,
		nullString(run.TriggerKey),
		run.Status,
		run.StartedAtMS,
		nullPositiveInt64(run.FinishedAtMS),
		run.TotalFiles,
		run.ProbeSetCount,
		run.SampledCount,
		run.DisabledCount,
		run.EnabledCount,
		run.DeleteCount,
		run.DisableCount,
		run.EnableCount,
		run.ReauthCount,
		run.KeepCount,
		nullString(run.Error),
		run.SettingsJSON,
		run.CreatedAtMS,
		run.UpdatedAtMS,
	)
	if err != nil {
		return model.CodexInspectionRun{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.CodexInspectionRun{}, err
	}
	run.ID = id
	return run, nil
}

func (r *repository) UpdateRun(ctx context.Context, run model.CodexInspectionRun) error {
	if run.ID <= 0 {
		return errors.New("codex inspection run id is required")
	}
	run.UpdatedAtMS = time.Now().UnixMilli()
	if run.SettingsJSON == "" {
		run.SettingsJSON = model.MarshalCodexInspectionSettings(run.Settings)
	}
	_, err := r.db.ExecContext(
		ctx,
		`update codex_inspection_runs set
			status = ?,
			finished_at_ms = ?,
			total_files = ?,
			probe_set_count = ?,
			sampled_count = ?,
			disabled_count = ?,
			enabled_count = ?,
			delete_count = ?,
			disable_count = ?,
			enable_count = ?,
			reauth_count = ?,
			keep_count = ?,
			error = ?,
			settings_json = ?,
			updated_at_ms = ?
		where id = ?`,
		run.Status,
		nullPositiveInt64(run.FinishedAtMS),
		run.TotalFiles,
		run.ProbeSetCount,
		run.SampledCount,
		run.DisabledCount,
		run.EnabledCount,
		run.DeleteCount,
		run.DisableCount,
		run.EnableCount,
		run.ReauthCount,
		run.KeepCount,
		nullString(run.Error),
		run.SettingsJSON,
		run.UpdatedAtMS,
		run.ID,
	)
	return err
}

func (r *repository) InsertResult(ctx context.Context, result model.CodexInspectionResult) (model.CodexInspectionResult, error) {
	if result.CreatedAtMS <= 0 {
		result.CreatedAtMS = time.Now().UnixMilli()
	}
	if result.QuotaWindowsJSON == "" && len(result.QuotaWindows) > 0 {
		result.QuotaWindowsJSON = model.MarshalCodexInspectionQuotaWindows(result.QuotaWindows)
	}
	result.ActionStatus = model.NormalizeCodexInspectionActionStatus(result.ActionStatus, result.Action)
	disabled := 0
	if result.Disabled {
		disabled = 1
	}
	isQuota := 0
	if result.IsQuota {
		isQuota = 1
	}
	autoRecoverEligible := 0
	if result.AutoRecoverEligible {
		autoRecoverEligible = 1
	}
	res, err := r.db.ExecContext(
		ctx,
		`insert into codex_inspection_results (
			run_id, account_key, file_name, display_account, auth_index, account_id,
			provider, disabled, status, state, action, action_reason, status_code,
			used_percent, is_quota, auto_recover_eligible, error, action_status, executed_action, action_error,
			plan_type, quota_windows_json, error_kind, error_detail, created_at_ms
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on conflict(run_id, account_key) do update set
			file_name = excluded.file_name,
			display_account = excluded.display_account,
			auth_index = excluded.auth_index,
			account_id = excluded.account_id,
			provider = excluded.provider,
			disabled = excluded.disabled,
			status = excluded.status,
			state = excluded.state,
			action = excluded.action,
			action_reason = excluded.action_reason,
			status_code = excluded.status_code,
			used_percent = excluded.used_percent,
			is_quota = excluded.is_quota,
			auto_recover_eligible = excluded.auto_recover_eligible,
			error = excluded.error,
			action_status = excluded.action_status,
			executed_action = excluded.executed_action,
			action_error = excluded.action_error,
			plan_type = excluded.plan_type,
			quota_windows_json = excluded.quota_windows_json,
			error_kind = excluded.error_kind,
			error_detail = excluded.error_detail,
			created_at_ms = excluded.created_at_ms`,
		result.RunID,
		result.AccountKey,
		result.FileName,
		result.DisplayAccount,
		nullString(result.AuthIndex),
		nullString(result.AccountID),
		nullString(result.Provider),
		disabled,
		nullString(result.Status),
		nullString(result.State),
		result.Action,
		nullString(result.ActionReason),
		nullInt(result.StatusCode),
		nullFloat(result.UsedPercent),
		isQuota,
		autoRecoverEligible,
		nullString(result.Error),
		nullString(result.ActionStatus),
		nullString(result.ExecutedAction),
		nullString(result.ActionError),
		nullString(result.PlanType),
		nullString(result.QuotaWindowsJSON),
		nullString(result.ErrorKind),
		nullString(result.ErrorDetail),
		result.CreatedAtMS,
	)
	if err != nil {
		return model.CodexInspectionResult{}, err
	}
	id, _ := res.LastInsertId()
	result.ID = id
	return result, nil
}

func (r *repository) InsertLog(ctx context.Context, entry model.CodexInspectionLog) (model.CodexInspectionLog, error) {
	if entry.CreatedAtMS <= 0 {
		entry.CreatedAtMS = time.Now().UnixMilli()
	}
	if entry.DetailJSON == "" && entry.Detail != nil {
		if data, err := json.Marshal(entry.Detail); err == nil {
			entry.DetailJSON = string(data)
		}
	}
	res, err := r.db.ExecContext(
		ctx,
		`insert into codex_inspection_logs(run_id, level, message, detail_json, created_at_ms)
		 values(?, ?, ?, ?, ?)`,
		entry.RunID,
		entry.Level,
		entry.Message,
		nullString(entry.DetailJSON),
		entry.CreatedAtMS,
	)
	if err != nil {
		return model.CodexInspectionLog{}, err
	}
	id, _ := res.LastInsertId()
	entry.ID = id
	return entry, nil
}

func (r *repository) ListRuns(ctx context.Context, limit int) ([]model.CodexInspectionRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.QueryContext(
		ctx,
		`select
			id, trigger_type, trigger_key, status, started_at_ms, finished_at_ms,
			total_files, probe_set_count, sampled_count, disabled_count, enabled_count,
			delete_count, disable_count, enable_count, reauth_count, keep_count, error,
			settings_json, created_at_ms, updated_at_ms
		from codex_inspection_runs
		order by started_at_ms desc, id desc
		limit ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	runs := make([]model.CodexInspectionRun, 0)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (r *repository) GetRun(ctx context.Context, id int64) (model.CodexInspectionRun, bool, error) {
	row := r.db.QueryRowContext(
		ctx,
		`select
			id, trigger_type, trigger_key, status, started_at_ms, finished_at_ms,
			total_files, probe_set_count, sampled_count, disabled_count, enabled_count,
			delete_count, disable_count, enable_count, reauth_count, keep_count, error,
			settings_json, created_at_ms, updated_at_ms
		from codex_inspection_runs
		where id = ?`,
		id,
	)
	run, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.CodexInspectionRun{}, false, nil
	}
	if err != nil {
		return model.CodexInspectionRun{}, false, err
	}
	return run, true, nil
}

func (r *repository) GetLatestRunByTrigger(ctx context.Context, triggerType, triggerKey string) (model.CodexInspectionRun, bool, error) {
	row := r.db.QueryRowContext(
		ctx,
		`select
			id, trigger_type, trigger_key, status, started_at_ms, finished_at_ms,
			total_files, probe_set_count, sampled_count, disabled_count, enabled_count,
			delete_count, disable_count, enable_count, reauth_count, keep_count, error,
			settings_json, created_at_ms, updated_at_ms
		from codex_inspection_runs
		where trigger_type = ? and trigger_key = ?
		order by started_at_ms desc, id desc
		limit 1`,
		triggerType,
		triggerKey,
	)
	run, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.CodexInspectionRun{}, false, nil
	}
	if err != nil {
		return model.CodexInspectionRun{}, false, err
	}
	return run, true, nil
}

func (r *repository) ListResults(ctx context.Context, runID int64) ([]model.CodexInspectionResult, error) {
	rows, err := r.db.QueryContext(
		ctx,
		`select
			id, run_id, account_key, file_name, display_account, auth_index, account_id,
			provider, disabled, status, state, action, action_reason, status_code,
			used_percent, is_quota, auto_recover_eligible, error, action_status, executed_action, action_error,
			plan_type, quota_windows_json, error_kind, error_detail, created_at_ms
		from codex_inspection_results
		where run_id = ?
		order by file_name asc, display_account asc, id asc`,
		runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make([]model.CodexInspectionResult, 0)
	for rows.Next() {
		result, err := scanResult(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

func (r *repository) ListDisableOwnership(ctx context.Context) ([]model.CodexInspectionDisableOwnership, error) {
	rows, err := r.db.QueryContext(ctx, `select file_name, provider, auth_index, account_id, disabled_at_ms, updated_at_ms
		from codex_inspection_disable_ownership order by file_name asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]model.CodexInspectionDisableOwnership, 0)
	for rows.Next() {
		var item model.CodexInspectionDisableOwnership
		var provider, authIndex, accountID sql.NullString
		if err := rows.Scan(&item.FileName, &provider, &authIndex, &accountID, &item.DisabledAtMS, &item.UpdatedAtMS); err != nil {
			return nil, err
		}
		item.Provider = provider.String
		item.AuthIndex = authIndex.String
		item.AccountID = accountID.String
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *repository) UpsertDisableOwnership(ctx context.Context, item model.CodexInspectionDisableOwnership) error {
	if item.FileName == "" {
		return errors.New("codex inspection ownership file name is required")
	}
	now := time.Now().UnixMilli()
	if item.DisabledAtMS <= 0 {
		item.DisabledAtMS = now
	}
	item.UpdatedAtMS = now
	provider := item.Provider
	if provider == "" {
		provider = "codex"
	}
	_, err := r.db.ExecContext(ctx, `insert into codex_inspection_disable_ownership (
		file_name, provider, auth_index, account_id, disabled_at_ms, updated_at_ms
	) values (?, ?, ?, ?, ?, ?)
	on conflict(file_name) do update set
		provider = excluded.provider,
		auth_index = excluded.auth_index,
		account_id = excluded.account_id,
		disabled_at_ms = excluded.disabled_at_ms,
		updated_at_ms = excluded.updated_at_ms`,
		item.FileName,
		provider,
		nullString(item.AuthIndex),
		nullString(item.AccountID),
		item.DisabledAtMS,
		item.UpdatedAtMS,
	)
	return err
}

func (r *repository) DeleteDisableOwnership(ctx context.Context, fileName string) error {
	if fileName == "" {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `delete from codex_inspection_disable_ownership where file_name = ?`, fileName)
	return err
}

func (r *repository) RevokeDisableOwnership(ctx context.Context, fileNames []string, clearAll bool) ([]model.CodexInspectionDisableOwnership, error) {
	if !clearAll && len(fileNames) == 0 {
		return nil, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	targets := make(map[string]struct{}, len(fileNames))
	for _, fileName := range fileNames {
		if fileName != "" {
			targets[fileName] = struct{}{}
		}
	}
	rows, err := tx.QueryContext(ctx, `select file_name, provider, auth_index, account_id, disabled_at_ms, updated_at_ms
		from codex_inspection_disable_ownership`)
	if err != nil {
		return nil, err
	}
	items := make([]model.CodexInspectionDisableOwnership, 0)
	for rows.Next() {
		var item model.CodexInspectionDisableOwnership
		var provider, authIndex, accountID sql.NullString
		if err := rows.Scan(&item.FileName, &provider, &authIndex, &accountID, &item.DisabledAtMS, &item.UpdatedAtMS); err != nil {
			_ = rows.Close()
			return nil, err
		}
		item.Provider = provider.String
		item.AuthIndex = authIndex.String
		item.AccountID = accountID.String
		if !clearAll {
			if _, ok := targets[item.FileName]; !ok {
				continue
			}
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	if clearAll {
		_, err = tx.ExecContext(ctx, `delete from codex_inspection_disable_ownership`)
	} else {
		for _, item := range items {
			if _, err = tx.ExecContext(ctx, `delete from codex_inspection_disable_ownership where file_name = ?`, item.FileName); err != nil {
				return nil, err
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func (r *repository) RestoreDisableOwnership(ctx context.Context, items []model.CodexInspectionDisableOwnership) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, item := range items {
		if item.FileName == "" {
			continue
		}
		if item.DisabledAtMS <= 0 {
			item.DisabledAtMS = time.Now().UnixMilli()
		}
		item.UpdatedAtMS = time.Now().UnixMilli()
		provider := item.Provider
		if provider == "" {
			provider = "codex"
		}
		if _, err := tx.ExecContext(ctx, `insert into codex_inspection_disable_ownership (
			file_name, provider, auth_index, account_id, disabled_at_ms, updated_at_ms
		) values (?, ?, ?, ?, ?, ?)
		on conflict(file_name) do nothing`,
			item.FileName,
			provider,
			nullString(item.AuthIndex),
			nullString(item.AccountID),
			item.DisabledAtMS,
			item.UpdatedAtMS,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *repository) ListLogs(ctx context.Context, runID int64) ([]model.CodexInspectionLog, error) {
	rows, err := r.db.QueryContext(
		ctx,
		`select id, run_id, level, message, detail_json, created_at_ms
		from codex_inspection_logs
		where run_id = ?
		order by created_at_ms asc, id asc`,
		runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	logs := make([]model.CodexInspectionLog, 0)
	for rows.Next() {
		entry, err := scanLog(rows)
		if err != nil {
			return nil, err
		}
		logs = append(logs, entry)
	}
	return logs, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRun(row scanner) (model.CodexInspectionRun, error) {
	var run model.CodexInspectionRun
	var triggerKey, errorText sql.NullString
	var finishedAt sql.NullInt64
	if err := row.Scan(
		&run.ID,
		&run.TriggerType,
		&triggerKey,
		&run.Status,
		&run.StartedAtMS,
		&finishedAt,
		&run.TotalFiles,
		&run.ProbeSetCount,
		&run.SampledCount,
		&run.DisabledCount,
		&run.EnabledCount,
		&run.DeleteCount,
		&run.DisableCount,
		&run.EnableCount,
		&run.ReauthCount,
		&run.KeepCount,
		&errorText,
		&run.SettingsJSON,
		&run.CreatedAtMS,
		&run.UpdatedAtMS,
	); err != nil {
		return model.CodexInspectionRun{}, err
	}
	run.TriggerKey = triggerKey.String
	run.Error = errorText.String
	if finishedAt.Valid {
		run.FinishedAtMS = finishedAt.Int64
	}
	run.Settings = model.UnmarshalCodexInspectionSettings(run.SettingsJSON)
	return run, nil
}

func scanResult(row scanner) (model.CodexInspectionResult, error) {
	var result model.CodexInspectionResult
	var authIndex, accountID, provider, status, state, actionReason, errorText sql.NullString
	var actionStatus, executedAction, actionError sql.NullString
	var planType, quotaWindowsJSON, errorKind, errorDetail sql.NullString
	var statusCode sql.NullInt64
	var usedPercent sql.NullFloat64
	var disabled, isQuota, autoRecoverEligible int
	if err := row.Scan(
		&result.ID,
		&result.RunID,
		&result.AccountKey,
		&result.FileName,
		&result.DisplayAccount,
		&authIndex,
		&accountID,
		&provider,
		&disabled,
		&status,
		&state,
		&result.Action,
		&actionReason,
		&statusCode,
		&usedPercent,
		&isQuota,
		&autoRecoverEligible,
		&errorText,
		&actionStatus,
		&executedAction,
		&actionError,
		&planType,
		&quotaWindowsJSON,
		&errorKind,
		&errorDetail,
		&result.CreatedAtMS,
	); err != nil {
		return model.CodexInspectionResult{}, err
	}
	result.AuthIndex = authIndex.String
	result.AccountID = accountID.String
	result.Provider = provider.String
	result.Disabled = disabled != 0
	result.Status = status.String
	result.State = state.String
	result.ActionReason = actionReason.String
	result.IsQuota = isQuota != 0
	result.AutoRecoverEligible = autoRecoverEligible != 0
	result.Error = errorText.String
	result.ActionStatus = model.NormalizeCodexInspectionActionStatus(actionStatus.String, result.Action)
	result.ExecutedAction = executedAction.String
	result.ActionError = actionError.String
	result.PlanType = planType.String
	result.QuotaWindowsJSON = quotaWindowsJSON.String
	result.QuotaWindows = model.UnmarshalCodexInspectionQuotaWindows(result.QuotaWindowsJSON)
	result.ErrorKind = errorKind.String
	result.ErrorDetail = errorDetail.String
	if statusCode.Valid {
		value := int(statusCode.Int64)
		result.StatusCode = &value
	}
	if usedPercent.Valid {
		value := usedPercent.Float64
		result.UsedPercent = &value
	}
	return result, nil
}

func scanLog(row scanner) (model.CodexInspectionLog, error) {
	var entry model.CodexInspectionLog
	var detail sql.NullString
	if err := row.Scan(
		&entry.ID,
		&entry.RunID,
		&entry.Level,
		&entry.Message,
		&detail,
		&entry.CreatedAtMS,
	); err != nil {
		return model.CodexInspectionLog{}, err
	}
	entry.DetailJSON = detail.String
	if detail.Valid && detail.String != "" {
		var parsed any
		if err := json.Unmarshal([]byte(detail.String), &parsed); err == nil {
			entry.Detail = parsed
		}
	}
	return entry, nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullPositiveInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func nullInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}
