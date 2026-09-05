package codexinspection

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

type Repository interface {
	// CreateRun imports a non-active historical run. Executable runs must be
	// created through AcquireRun so the run row and global lease are committed
	// atomically.
	CreateRun(ctx context.Context, run model.CodexInspectionRun) (model.CodexInspectionRun, error)
	// UpdateRun refreshes summary fields on an existing terminal run without
	// permitting a terminal-state transition or touching an active lifecycle.
	UpdateRun(ctx context.Context, run model.CodexInspectionRun) error
	UpdateRunProgress(ctx context.Context, run model.CodexInspectionRun, ownerID string) error
	AcquireRun(ctx context.Context, run model.CodexInspectionRun, ownerID string, leaseDuration time.Duration) (AcquireRunResult, error)
	HeartbeatRun(ctx context.Context, runID int64, ownerID string, leaseDuration time.Duration) error
	MarkRunCancelling(ctx context.Context, runID int64, ownerID string, reason string) (bool, error)
	FinalizeRun(ctx context.Context, run model.CodexInspectionRun, ownerID string, finalLog *model.CodexInspectionLog) error
	// ForceFinalizeRun is the fenced recovery path for a worker that lost an
	// unexpired lease while it was finishing. It may finalize only while the
	// expired lease still belongs to the same run/owner, so a replacement
	// instance can never be overwritten by a stale worker.
	ForceFinalizeRun(ctx context.Context, run model.CodexInspectionRun, ownerID string, finalLog *model.CodexInspectionLog) error
	GetActiveLease(ctx context.Context, nowMS int64) (model.CodexInspectionLease, bool, error)
	RecoverStaleRuns(ctx context.Context, nowMS int64, reason string) ([]model.CodexInspectionRun, error)
	InsertResult(ctx context.Context, result model.CodexInspectionResult) (model.CodexInspectionResult, error)
	InsertLog(ctx context.Context, entry model.CodexInspectionLog) (model.CodexInspectionLog, error)
	ListRuns(ctx context.Context, limit int) ([]model.CodexInspectionRun, error)
	GetRun(ctx context.Context, id int64) (model.CodexInspectionRun, bool, error)
	GetLatestRunByTrigger(ctx context.Context, triggerType, triggerKey string) (model.CodexInspectionRun, bool, error)
	GetLatestRunByTriggerType(ctx context.Context, triggerType string) (model.CodexInspectionRun, bool, error)
	ListResults(ctx context.Context, runID int64) ([]model.CodexInspectionResult, error)
	ListLogs(ctx context.Context, runID int64) ([]model.CodexInspectionLog, error)
	ListDisableOwnership(ctx context.Context) ([]model.CodexInspectionDisableOwnership, error)
	UpsertDisableOwnership(ctx context.Context, item model.CodexInspectionDisableOwnership) error
	UpsertDisableOwnerships(ctx context.Context, items []model.CodexInspectionDisableOwnership) error
	DeleteDisableOwnership(ctx context.Context, target model.CodexInspectionDisableOwnershipTarget) error
	RevokeDisableOwnership(ctx context.Context, targets []model.CodexInspectionDisableOwnershipTarget, clearAll bool) ([]model.CodexInspectionDisableOwnership, error)
	RestoreDisableOwnership(ctx context.Context, items []model.CodexInspectionDisableOwnership) error
}

type AcquireRunResult struct {
	Run          model.CodexInspectionRun
	RecoveredRun *model.CodexInspectionRun
}

var (
	ErrLeaseAlreadyActive     = errors.New("codex inspection lease is already active")
	ErrTriggerAlreadyExists   = errors.New("codex inspection trigger already exists")
	ErrLeaseLost              = errors.New("codex inspection lease is no longer owned")
	ErrInvalidFinalStatus     = errors.New("codex inspection terminal status is invalid")
	ErrActiveRunRequiresLease = errors.New("active codex inspection runs must be created and updated through the lease lifecycle")
	ErrRunStateConflict       = errors.New("codex inspection run state changed concurrently")
)

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
	run.Status = model.NormalizeCodexInspectionRunStatus(run.Status)
	if run.Status == "" || model.IsCodexInspectionRunActive(run.Status) {
		return model.CodexInspectionRun{}, ErrActiveRunRequiresLease
	}
	if run.SettingsJSON == "" {
		run.SettingsJSON = model.MarshalCodexInspectionSettings(run.Settings)
	}
	var res sql.Result
	err := withSQLiteBusyRetry(ctx, func() error {
		var err error
		res, err = r.db.ExecContext(
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
		return err
	})
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
	run.Status = model.NormalizeCodexInspectionRunStatus(run.Status)
	if run.Status == "" || model.IsCodexInspectionRunActive(run.Status) {
		return ErrActiveRunRequiresLease
	}
	run.UpdatedAtMS = time.Now().UnixMilli()
	if run.SettingsJSON == "" {
		run.SettingsJSON = model.MarshalCodexInspectionSettings(run.Settings)
	}
	err := withSQLiteBusyRetry(ctx, func() error {
		res, err := r.db.ExecContext(
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
			where id = ? and status = ? and status not in (?, ?)`,
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
			run.Status,
			model.CodexInspectionStatusRunning,
			model.CodexInspectionStatusCancelling,
		)
		if err != nil {
			return err
		}
		changed, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return ErrRunStateConflict
		}
		return nil
	})
	return err
}

func (r *repository) InsertResult(ctx context.Context, result model.CodexInspectionResult) (model.CodexInspectionResult, error) {
	if result.CreatedAtMS <= 0 {
		result.CreatedAtMS = time.Now().UnixMilli()
	}
	quotaWindowsJSON := strings.TrimSpace(result.QuotaWindowsJSON)
	if quotaWindowsJSON == "" {
		switch {
		case len(result.QuotaWindows) > 0:
			quotaWindowsJSON = model.MarshalCodexInspectionQuotaWindows(result.QuotaWindows)
		case result.QuotaInventoryObserved:
			quotaWindowsJSON = "[]"
		}
	}
	quotaWindows, quotaInventoryObserved := model.ParseCodexInspectionQuotaWindows(quotaWindowsJSON)
	if quotaInventoryObserved {
		quotaWindowsJSON = model.MarshalCodexInspectionQuotaWindows(quotaWindows)
		if len(quotaWindows) == 0 {
			quotaWindowsJSON = "[]"
		}
	}
	result.QuotaWindowsJSON = quotaWindowsJSON
	result.QuotaWindows = quotaWindows
	result.QuotaInventoryObserved = quotaInventoryObserved
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
	var id int64
	err := withSQLiteBusyRetry(ctx, func() error {
		return r.db.QueryRowContext(
			ctx,
			`insert into codex_inspection_results (
			run_id, account_key, file_name, display_account, account_snapshot, auth_index, account_id,
			provider, disabled, status, state, action, action_reason, status_code,
			used_percent, is_quota, auto_recover_eligible, error, action_status, executed_action, action_error,
			plan_type, quota_windows_json, error_kind, error_detail, created_at_ms
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on conflict(run_id, account_key) do update set
			file_name = excluded.file_name,
			display_account = excluded.display_account,
			account_snapshot = excluded.account_snapshot,
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
			quota_windows_json = case
				when excluded.quota_windows_json is not null then excluded.quota_windows_json
				else codex_inspection_results.quota_windows_json
			end,
				error_kind = excluded.error_kind,
				error_detail = excluded.error_detail,
				created_at_ms = excluded.created_at_ms
			returning id`,
			result.RunID,
			result.AccountKey,
			result.FileName,
			result.DisplayAccount,
			nullString(result.AccountSnapshot),
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
			nullStringIf(result.QuotaInventoryObserved, result.QuotaWindowsJSON),
			nullString(result.ErrorKind),
			nullString(result.ErrorDetail),
			result.CreatedAtMS,
		).Scan(&id)
	})
	if err != nil {
		return model.CodexInspectionResult{}, err
	}
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
	var id int64
	err := withSQLiteBusyRetry(ctx, func() error {
		return r.db.QueryRowContext(
			ctx,
			`insert into codex_inspection_logs(run_id, level, message, detail_json, created_at_ms)
			 values(?, ?, ?, ?, ?)
			 returning id`,
			entry.RunID,
			entry.Level,
			entry.Message,
			nullString(entry.DetailJSON),
			entry.CreatedAtMS,
		).Scan(&id)
	})
	if err != nil {
		return model.CodexInspectionLog{}, err
	}
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

func (r *repository) GetLatestRunByTriggerType(ctx context.Context, triggerType string) (model.CodexInspectionRun, bool, error) {
	row := r.db.QueryRowContext(
		ctx,
		`select
			id, trigger_type, trigger_key, status, started_at_ms, finished_at_ms,
			total_files, probe_set_count, sampled_count, disabled_count, enabled_count,
			delete_count, disable_count, enable_count, reauth_count, keep_count, error,
			settings_json, created_at_ms, updated_at_ms
		from codex_inspection_runs
		where trigger_type = ?
		order by started_at_ms desc, id desc
		limit 1`,
		triggerType,
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
			id, run_id, account_key, file_name, display_account, account_snapshot, auth_index, account_id,
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
	rows, err := r.db.QueryContext(ctx, `select file_name, provider, auth_index, account_id, account_snapshot, disabled_at_ms, updated_at_ms
		from codex_inspection_disable_ownership
		order by file_name asc, provider asc, auth_index asc, account_id asc, account_snapshot asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]model.CodexInspectionDisableOwnership, 0)
	for rows.Next() {
		var item model.CodexInspectionDisableOwnership
		var provider, authIndex, accountID, accountSnapshot sql.NullString
		if err := rows.Scan(&item.FileName, &provider, &authIndex, &accountID, &accountSnapshot, &item.DisabledAtMS, &item.UpdatedAtMS); err != nil {
			return nil, err
		}
		item.Provider = provider.String
		item.AuthIndex = authIndex.String
		item.AccountID = accountID.String
		item.AccountSnapshot = accountSnapshot.String
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *repository) UpsertDisableOwnership(ctx context.Context, item model.CodexInspectionDisableOwnership) error {
	return r.UpsertDisableOwnerships(ctx, []model.CodexInspectionDisableOwnership{item})
}

func (r *repository) UpsertDisableOwnerships(ctx context.Context, items []model.CodexInspectionDisableOwnership) error {
	if len(items) == 0 {
		return nil
	}
	normalized := make([]model.CodexInspectionDisableOwnership, len(items))
	now := time.Now().UnixMilli()
	for index, item := range items {
		item = normalizeDisableOwnership(item)
		if item.FileName == "" {
			return errors.New("codex inspection ownership file name is required")
		}
		if item.DisabledAtMS <= 0 {
			item.DisabledAtMS = now
		}
		item.UpdatedAtMS = now
		normalized[index] = item
	}
	return withSQLiteBusyRetry(ctx, func() error {
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		for _, item := range normalized {
			if _, err := tx.ExecContext(ctx, `insert into codex_inspection_disable_ownership (
				file_name, provider, auth_index, account_id, account_snapshot, disabled_at_ms, updated_at_ms
			) values (?, ?, ?, ?, ?, ?, ?)
			on conflict(file_name, provider, auth_index, account_id, account_snapshot) do update set
				disabled_at_ms = excluded.disabled_at_ms,
				updated_at_ms = excluded.updated_at_ms`,
				item.FileName,
				item.Provider,
				item.AuthIndex,
				item.AccountID,
				item.AccountSnapshot,
				item.DisabledAtMS,
				item.UpdatedAtMS,
			); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

func (r *repository) DeleteDisableOwnership(ctx context.Context, target model.CodexInspectionDisableOwnershipTarget) error {
	if strings.TrimSpace(target.FileName) == "" {
		return nil
	}
	_, err := r.RevokeDisableOwnership(ctx, []model.CodexInspectionDisableOwnershipTarget{target}, false)
	return err
}

func (r *repository) RevokeDisableOwnership(ctx context.Context, targets []model.CodexInspectionDisableOwnershipTarget, clearAll bool) ([]model.CodexInspectionDisableOwnership, error) {
	if !clearAll && len(targets) == 0 {
		return nil, nil
	}
	var revoked []model.CodexInspectionDisableOwnership
	err := withSQLiteBusyRetry(ctx, func() error {
		items, err := r.revokeDisableOwnershipOnce(ctx, targets, clearAll)
		if err != nil {
			revoked = nil
			return err
		}
		revoked = items
		return nil
	})
	return revoked, err
}

func (r *repository) revokeDisableOwnershipOnce(ctx context.Context, targets []model.CodexInspectionDisableOwnershipTarget, clearAll bool) ([]model.CodexInspectionDisableOwnership, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `select file_name, provider, auth_index, account_id, account_snapshot, disabled_at_ms, updated_at_ms
		from codex_inspection_disable_ownership`)
	if err != nil {
		return nil, err
	}
	items := make([]model.CodexInspectionDisableOwnership, 0)
	for rows.Next() {
		var item model.CodexInspectionDisableOwnership
		var provider, authIndex, accountID, accountSnapshot sql.NullString
		if err := rows.Scan(&item.FileName, &provider, &authIndex, &accountID, &accountSnapshot, &item.DisabledAtMS, &item.UpdatedAtMS); err != nil {
			_ = rows.Close()
			return nil, err
		}
		item.Provider = provider.String
		item.AuthIndex = authIndex.String
		item.AccountID = accountID.String
		item.AccountSnapshot = accountSnapshot.String
		item = normalizeDisableOwnership(item)
		if !clearAll && !disableOwnershipMatchesAnyTarget(item, targets) {
			continue
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
			if _, err = tx.ExecContext(ctx, `delete from codex_inspection_disable_ownership
				where file_name = ? and provider = ? and auth_index = ? and account_id = ? and account_snapshot = ?`,
				item.FileName, item.Provider, item.AuthIndex, item.AccountID, item.AccountSnapshot); err != nil {
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
	return withSQLiteBusyRetry(ctx, func() error {
		return r.restoreDisableOwnershipOnce(ctx, items)
	})
}

func (r *repository) restoreDisableOwnershipOnce(ctx context.Context, items []model.CodexInspectionDisableOwnership) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, item := range items {
		item = normalizeDisableOwnership(item)
		if item.FileName == "" {
			continue
		}
		if item.DisabledAtMS <= 0 {
			item.DisabledAtMS = time.Now().UnixMilli()
		}
		item.UpdatedAtMS = time.Now().UnixMilli()
		if _, err := tx.ExecContext(ctx, `insert into codex_inspection_disable_ownership (
			file_name, provider, auth_index, account_id, account_snapshot, disabled_at_ms, updated_at_ms
		) values (?, ?, ?, ?, ?, ?, ?)
		on conflict(file_name, provider, auth_index, account_id, account_snapshot) do nothing`,
			item.FileName,
			item.Provider,
			item.AuthIndex,
			item.AccountID,
			item.AccountSnapshot,
			item.DisabledAtMS,
			item.UpdatedAtMS,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func normalizeDisableOwnership(item model.CodexInspectionDisableOwnership) model.CodexInspectionDisableOwnership {
	item.FileName = strings.TrimSpace(item.FileName)
	item.Provider = normalizeDisableOwnershipProvider(item.Provider)
	item.AuthIndex = strings.TrimSpace(item.AuthIndex)
	item.AccountID = strings.TrimSpace(item.AccountID)
	item.AccountSnapshot = strings.TrimSpace(item.AccountSnapshot)
	if item.Provider == "codex" {
		if member, ok := usageidentity.NormalizeCodexMemberSnapshot(item.AccountSnapshot); ok {
			item.AccountSnapshot = member
		} else {
			item.AccountSnapshot = ""
		}
		return item
	}
	if item.AccountID != "" {
		item.AccountSnapshot = ""
	}
	return item
}

func normalizeDisableOwnershipProvider(value string) string {
	provider := strings.ToLower(strings.TrimSpace(value))
	provider = strings.ReplaceAll(provider, "_", "-")
	switch provider {
	case "x-ai", "grok":
		return "xai"
	default:
		return provider
	}
}

func disableOwnershipMatchesAnyTarget(item model.CodexInspectionDisableOwnership, targets []model.CodexInspectionDisableOwnershipTarget) bool {
	for _, target := range targets {
		if disableOwnershipMatchesTarget(item, target) {
			return true
		}
	}
	return false
}

func disableOwnershipMatchesTarget(item model.CodexInspectionDisableOwnership, target model.CodexInspectionDisableOwnershipTarget) bool {
	if item.FileName != strings.TrimSpace(target.FileName) {
		return false
	}
	if target.Provider != nil {
		provider := normalizeDisableOwnershipProvider(*target.Provider)
		if item.Provider != "" && item.Provider != provider {
			return false
		}
		if provider == "codex" {
			return disableOwnershipMatchesCodexTarget(item, target)
		}
	}
	if target.AuthIndex != nil {
		authIndex := strings.TrimSpace(*target.AuthIndex)
		if authIndex == "" {
			if item.AuthIndex != "" {
				return false
			}
		} else if item.AuthIndex != "" && item.AuthIndex != authIndex {
			return false
		}
	}
	if target.AccountID != nil {
		accountID := strings.TrimSpace(*target.AccountID)
		if accountID == "" {
			if item.AccountID != "" {
				return false
			}
			if target.AccountSnapshot == nil && item.AccountSnapshot != "" {
				return false
			}
		} else if item.AccountID != "" {
			if item.AccountID != accountID {
				return false
			}
		} else if item.AccountSnapshot != "" {
			if target.AccountSnapshot == nil || strings.TrimSpace(*target.AccountSnapshot) != item.AccountSnapshot {
				return false
			}
		}
	}
	if (target.AccountID == nil || strings.TrimSpace(*target.AccountID) == "") && target.AccountSnapshot != nil {
		accountSnapshot := strings.TrimSpace(*target.AccountSnapshot)
		if accountSnapshot == "" {
			if item.AccountSnapshot != "" {
				return false
			}
		} else if item.AccountSnapshot != "" && item.AccountSnapshot != accountSnapshot {
			return false
		}
	}
	return true
}

func disableOwnershipMatchesCodexTarget(item model.CodexInspectionDisableOwnership, target model.CodexInspectionDisableOwnershipTarget) bool {
	if item.Provider != "" && item.Provider != "codex" {
		return false
	}

	// This matcher is used for explicit cleanup, not recovery authorization.
	// A concrete credential may therefore remove a weaker legacy record, but it
	// must never remove a different member's workspace-qualified record.
	if target.AuthIndex != nil {
		targetAuthIndex := strings.TrimSpace(*target.AuthIndex)
		if targetAuthIndex == "" {
			if item.AuthIndex != "" {
				return false
			}
		} else if item.AuthIndex != "" && item.AuthIndex != targetAuthIndex {
			return false
		}
	}

	targetAccountIDSet := target.AccountID != nil
	targetAccountID := ""
	if targetAccountIDSet {
		targetAccountID = strings.TrimSpace(*target.AccountID)
		if targetAccountID == "" {
			if item.AccountID != "" {
				return false
			}
		} else if item.AccountID != "" && item.AccountID != targetAccountID {
			return false
		}
	} else if item.AccountID != "" {
		// Without a target workspace, a workspace-qualified ownership record is
		// not a compatible legacy wildcard.
		return false
	}

	targetMemberSet := target.AccountSnapshot != nil
	targetMember := ""
	if targetMemberSet {
		targetMember, _ = usageidentity.NormalizeCodexMemberSnapshot(*target.AccountSnapshot)
	}
	itemMember, _ := usageidentity.NormalizeCodexMemberSnapshot(item.AccountSnapshot)
	if targetMember == "" {
		// A target without reliable member evidence may only clean records that
		// also lack member evidence. This prevents Alice's action from deleting
		// Bob's member-specific ownership in a shared Workspace.
		return itemMember == ""
	}
	if itemMember != "" && itemMember != targetMember {
		return false
	}
	return true
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
	run.Status = model.NormalizeCodexInspectionRunStatus(run.Status)
	run.Error = errorText.String
	if finishedAt.Valid {
		run.FinishedAtMS = finishedAt.Int64
	}
	run.Settings = model.UnmarshalCodexInspectionSettings(run.SettingsJSON)
	return run, nil
}

func scanResult(row scanner) (model.CodexInspectionResult, error) {
	var result model.CodexInspectionResult
	var accountSnapshot, authIndex, accountID, provider, status, state, actionReason, errorText sql.NullString
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
		&accountSnapshot,
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
	result.AccountSnapshot = accountSnapshot.String
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
	result.QuotaWindows, result.QuotaInventoryObserved = model.ParseCodexInspectionQuotaWindows(result.QuotaWindowsJSON)
	if !result.QuotaInventoryObserved {
		result.QuotaWindowsJSON = ""
	}
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

func nullStringIf(ok bool, value string) any {
	if !ok {
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
