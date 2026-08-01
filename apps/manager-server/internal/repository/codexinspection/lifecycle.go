package codexinspection

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	modernsqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	defaultInspectionLeaseDuration = 30 * time.Second
	maxLifecycleBusyRetries        = 5
	recoveryLogWriteTimeout        = 2 * time.Second
	userCancelledFallbackReason    = "用户主动取消巡检"
)

// withSQLiteBusyRetry deliberately applies only to lifecycle writes. Ordinary
// reads are left untouched so a transient lock does not turn into unbounded
// query latency throughout the application.
func withSQLiteBusyRetry(ctx context.Context, operation func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var lastErr error
	for attempt := 0; attempt <= maxLifecycleBusyRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = operation()
		if lastErr == nil || !isSQLiteBusyError(lastErr) || attempt == maxLifecycleBusyRetries {
			return lastErr
		}
		backoff := time.Duration(1<<attempt) * 10 * time.Millisecond
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			stopSQLiteRetryTimer(timer)
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func stopSQLiteRetryTimer(timer *time.Timer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

func isSQLiteBusyError(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr *modernsqlite.Error
	if errors.As(err, &sqliteErr) {
		code := sqliteErr.Code() & 0xff
		return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "sqlite_busy") ||
		strings.Contains(message, "sqlite_locked") ||
		strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked")
}

func IsSQLiteBusyError(err error) bool {
	return isSQLiteBusyError(err)
}

func (r *repository) AcquireRun(
	ctx context.Context,
	run model.CodexInspectionRun,
	ownerID string,
	leaseDuration time.Duration,
) (result AcquireRunResult, err error) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return result, errors.New("codex inspection lease owner is required")
	}
	if leaseDuration <= 0 {
		leaseDuration = defaultInspectionLeaseDuration
	}
	// Callers cannot acquire a lease for a preselected terminal or unknown state. The
	// repository owns the executable state transition and always creates a fresh
	// running row without stale completion fields.
	run.Status = model.CodexInspectionStatusRunning
	run.FinishedAtMS = 0
	run.Error = ""
	err = withSQLiteBusyRetry(ctx, func() error {
		attemptResult, attemptErr := r.acquireRunOnce(ctx, run, ownerID, leaseDuration)
		if attemptErr == nil {
			result = attemptResult
		}
		return attemptErr
	})
	return result, err
}

func (r *repository) acquireRunOnce(
	ctx context.Context,
	run model.CodexInspectionRun,
	ownerID string,
	leaseDuration time.Duration,
) (AcquireRunResult, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return AcquireRunResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UnixMilli()
	leaseDurationMS := leaseDuration.Milliseconds()
	if leaseDurationMS <= 0 {
		leaseDurationMS = 1
	}
	var existing model.CodexInspectionLease
	var existingRunID sql.NullInt64
	reclaimTerminalLease := false
	err = tx.QueryRowContext(ctx, `select run_id, owner_id, heartbeat_at_ms, lease_expires_at_ms
		from codex_inspection_leases where id = 1`).Scan(
		&existingRunID,
		&existing.OwnerID,
		&existing.HeartbeatAtMS,
		&existing.LeaseExpiresAtMS,
	)
	hasLease := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AcquireRunResult{}, err
	}
	if hasLease {
		existing.RunID = existingRunID.Int64
		// A committed lease must normally be bound to a run in the same
		// transaction. If a process left an unbound row behind, it cannot
		// represent executable work and is safe to reclaim immediately.
		if existingRunID.Valid && existing.RunID > 0 && existing.LeaseExpiresAtMS > now {
			// A crash can happen after the terminal run update but before the
			// lease delete. Such a row is not executable work and must not block
			// every future inspection until the old lease timeout. Only an
			// unexpired lease bound to an active run fences a new acquisition.
			var existingStatus string
			if statusErr := tx.QueryRowContext(ctx, `select status from codex_inspection_runs where id = ?`, existing.RunID).Scan(&existingStatus); statusErr == nil {
				if model.IsCodexInspectionRunActive(existingStatus) {
					return AcquireRunResult{}, ErrLeaseAlreadyActive
				}
				reclaimTerminalLease = true
			} else if !errors.Is(statusErr, sql.ErrNoRows) {
				return AcquireRunResult{}, statusErr
			} else {
				reclaimTerminalLease = true
			}
		}
	}

	var recovered *model.CodexInspectionRun
	if hasLease && existing.RunID > 0 {
		var oldStatus string
		var oldStarted int64
		if err := tx.QueryRowContext(ctx, `select status, started_at_ms from codex_inspection_runs where id = ?`, existing.RunID).Scan(&oldStatus, &oldStarted); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return AcquireRunResult{}, err
		} else if err == nil && model.IsCodexInspectionRunActive(oldStatus) {
			finished := now
			message := "服务重启或任务租约过期，巡检已中断"
			statusResult, err := tx.ExecContext(ctx, `update codex_inspection_runs set status = ?, finished_at_ms = ?, error = ?, updated_at_ms = ? where id = ? and status in (?, ?) and exists (
				select 1 from codex_inspection_leases
				where id = 1 and run_id = ? and owner_id = ? and lease_expires_at_ms <= ?
			)`,
				model.CodexInspectionStatusInterrupted,
				finished,
				message,
				finished,
				existing.RunID,
				model.CodexInspectionStatusRunning,
				model.CodexInspectionStatusCancelling,
				existing.RunID,
				existing.OwnerID,
				now,
			)
			if err != nil {
				return AcquireRunResult{}, err
			}
			statusRows, err := statusResult.RowsAffected()
			if err != nil {
				return AcquireRunResult{}, err
			}
			if statusRows != 1 {
				// The old owner refreshed the lease (or another instance fenced
				// this run) between the snapshot and this update. Do not recover
				// the row or claim the lease based on stale state.
				return AcquireRunResult{}, ErrLeaseAlreadyActive
			}
			recoveredRun := model.CodexInspectionRun{
				ID:           existing.RunID,
				Status:       model.CodexInspectionStatusInterrupted,
				StartedAtMS:  oldStarted,
				FinishedAtMS: finished,
				Error:        message,
				UpdatedAtMS:  finished,
			}
			recovered = &recoveredRun
		}
	}

	// Compute the claim timestamp inside SQLite after any writer-lock wait. A Go
	// timestamp captured before sqlite3_step could make the freshly acquired
	// lease substantially shorter than the Service believes it is.
	if hasLease {
		res, err := tx.ExecContext(ctx, `update codex_inspection_leases set
			run_id = null,
			owner_id = ?,
			heartbeat_at_ms = cast(unixepoch('subsec') * 1000 as integer),
			lease_expires_at_ms = cast(unixepoch('subsec') * 1000 as integer) + ?
			where id = 1 and (
				run_id is null or run_id <= 0 or
				lease_expires_at_ms <= cast(unixepoch('subsec') * 1000 as integer) or
				? = 1
			)`, ownerID, leaseDurationMS, boolAsInt(reclaimTerminalLease))
		if err != nil {
			return AcquireRunResult{}, err
		}
		changed, err := res.RowsAffected()
		if err != nil {
			return AcquireRunResult{}, err
		}
		if changed != 1 {
			return AcquireRunResult{}, ErrLeaseAlreadyActive
		}
	} else {
		res, err := tx.ExecContext(ctx, `insert into codex_inspection_leases(
			id, run_id, owner_id, heartbeat_at_ms, lease_expires_at_ms
		) values (
			1,
			null,
			?,
			cast(unixepoch('subsec') * 1000 as integer),
			cast(unixepoch('subsec') * 1000 as integer) + ?
		) on conflict(id) do nothing`, ownerID, leaseDurationMS)
		if err != nil {
			return AcquireRunResult{}, err
		}
		changed, err := res.RowsAffected()
		if err != nil {
			return AcquireRunResult{}, err
		}
		if changed != 1 {
			return AcquireRunResult{}, ErrLeaseAlreadyActive
		}
	}
	var claimNow, claimExpires int64
	if err := tx.QueryRowContext(ctx, `select heartbeat_at_ms, lease_expires_at_ms
		from codex_inspection_leases
		where id = 1 and owner_id = ? and run_id is null`, ownerID).Scan(&claimNow, &claimExpires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AcquireRunResult{}, ErrLeaseLost
		}
		return AcquireRunResult{}, err
	}
	if claimExpires <= claimNow {
		return AcquireRunResult{}, ErrLeaseLost
	}

	triggerType := strings.TrimSpace(run.TriggerType)
	triggerKey := strings.TrimSpace(run.TriggerKey)
	if triggerType == model.CodexInspectionTriggerScheduled && triggerKey != "" {
		var existingTriggerID int64
		err := tx.QueryRowContext(ctx, `select id from codex_inspection_runs where trigger_type = ? and trigger_key = ? limit 1`, triggerType, triggerKey).Scan(&existingTriggerID)
		if err == nil {
			if _, deleteErr := tx.ExecContext(ctx, `delete from codex_inspection_leases where id = 1 and owner_id = ?`, ownerID); deleteErr != nil {
				return AcquireRunResult{}, deleteErr
			}
			if commitErr := tx.Commit(); commitErr != nil {
				return AcquireRunResult{}, commitErr
			}
			r.writeRecoveryLogBestEffort(ctx, recovered)
			return AcquireRunResult{}, ErrTriggerAlreadyExists
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return AcquireRunResult{}, err
		}
	}

	prepared := prepareRun(run, claimNow)
	var runID int64
	err = tx.QueryRowContext(ctx, `insert into codex_inspection_runs (
		trigger_type, trigger_key, status, started_at_ms, finished_at_ms,
		total_files, probe_set_count, sampled_count, disabled_count, enabled_count,
		delete_count, disable_count, enable_count, reauth_count, keep_count, error,
		settings_json, created_at_ms, updated_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	returning id`,
		prepared.TriggerType, nullString(prepared.TriggerKey), prepared.Status, prepared.StartedAtMS, nullPositiveInt64(prepared.FinishedAtMS),
		prepared.TotalFiles, prepared.ProbeSetCount, prepared.SampledCount, prepared.DisabledCount, prepared.EnabledCount,
		prepared.DeleteCount, prepared.DisableCount, prepared.EnableCount, prepared.ReauthCount, prepared.KeepCount,
		nullString(prepared.Error), prepared.SettingsJSON, prepared.CreatedAtMS, prepared.UpdatedAtMS,
	).Scan(&runID)
	if err != nil {
		return AcquireRunResult{}, err
	}
	prepared.ID = runID
	leaseResult, err := tx.ExecContext(ctx, `update codex_inspection_leases set run_id = ? where id = 1 and owner_id = ?`, runID, ownerID)
	if err != nil {
		return AcquireRunResult{}, err
	}
	leaseRows, err := leaseResult.RowsAffected()
	if err != nil {
		return AcquireRunResult{}, err
	}
	if leaseRows != 1 {
		return AcquireRunResult{}, ErrLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return AcquireRunResult{}, err
	}
	prepared.Active = true
	prepared.Cancellable = true
	result := AcquireRunResult{Run: prepared, RecoveredRun: recovered}
	// Recovery state and lease ownership are already committed. A diagnostic
	// log is best effort and must never make the newly acquired run appear to
	// have failed (which would strand its committed lease).
	r.writeRecoveryLogBestEffort(ctx, recovered)
	return result, nil
}

func (r *repository) writeRecoveryLogBestEffort(ctx context.Context, recovered *model.CodexInspectionRun) {
	if recovered == nil {
		return
	}
	logCtx, cancelLog := context.WithTimeout(context.WithoutCancel(ctx), recoveryLogWriteTimeout)
	_, logErr := r.InsertLog(logCtx, model.CodexInspectionLog{
		RunID:       recovered.ID,
		Level:       "warning",
		Message:     recovered.Error,
		Detail:      map[string]any{"reason": "lease_expired", "startedAtMs": recovered.StartedAtMS},
		CreatedAtMS: recovered.FinishedAtMS,
	})
	cancelLog()
	if logErr != nil {
		log.Printf("write codex inspection recovery log for run %d: %v", recovered.ID, logErr)
	}
}

func prepareRun(run model.CodexInspectionRun, now int64) model.CodexInspectionRun {
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
	return run
}

func boolAsInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (r *repository) HeartbeatRun(ctx context.Context, runID int64, ownerID string, leaseDuration time.Duration) error {
	if leaseDuration <= 0 {
		leaseDuration = defaultInspectionLeaseDuration
	}
	return withSQLiteBusyRetry(ctx, func() error {
		leaseDurationMS := leaseDuration.Milliseconds()
		if leaseDurationMS <= 0 {
			leaseDurationMS = 1
		}
		// Compute the timestamp inside SQLite after any writer-lock wait. A Go
		// timestamp captured before sqlite3_step could otherwise resurrect an
		// already expired lease or return with substantially less lifetime than the
		// heartbeat loop believes it renewed.
		res, err := r.db.ExecContext(ctx, `update codex_inspection_leases set
			heartbeat_at_ms = cast(unixepoch('subsec') * 1000 as integer),
			lease_expires_at_ms = cast(unixepoch('subsec') * 1000 as integer) + ?
			where id = 1 and run_id = ? and owner_id = ?
			and lease_expires_at_ms > cast(unixepoch('subsec') * 1000 as integer)
			and exists (
			select 1 from codex_inspection_runs where id = ? and status in (?, ?)
		)`, leaseDurationMS, runID, ownerID, runID, model.CodexInspectionStatusRunning, model.CodexInspectionStatusCancelling)
		if err != nil {
			return err
		}
		changed, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return ErrLeaseLost
		}
		return nil
	})
}

func (r *repository) UpdateRunProgress(ctx context.Context, run model.CodexInspectionRun, ownerID string) error {
	ownerID = strings.TrimSpace(ownerID)
	if run.ID <= 0 || ownerID == "" {
		return ErrLeaseLost
	}
	if run.SettingsJSON == "" {
		run.SettingsJSON = model.MarshalCodexInspectionSettings(run.Settings)
	}
	return withSQLiteBusyRetry(ctx, func() error {
		now := time.Now().UnixMilli()
		res, err := r.db.ExecContext(ctx, `update codex_inspection_runs set
			total_files = ?, probe_set_count = ?, sampled_count = ?, disabled_count = ?, enabled_count = ?,
			delete_count = ?, disable_count = ?, enable_count = ?, reauth_count = ?, keep_count = ?,
			settings_json = ?, updated_at_ms = ?
		where id = ? and status in (?, ?) and exists (
			select 1 from codex_inspection_leases
			where id = 1 and run_id = ? and owner_id = ? and lease_expires_at_ms > ?
		)`,
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
			run.SettingsJSON,
			now,
			run.ID,
			model.CodexInspectionStatusRunning,
			model.CodexInspectionStatusCancelling,
			run.ID,
			ownerID,
			now,
		)
		if err != nil {
			return err
		}
		changed, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return ErrLeaseLost
		}
		return nil
	})
}

func (r *repository) MarkRunCancelling(ctx context.Context, runID int64, ownerID string, reason string) (bool, error) {
	var changed bool
	err := withSQLiteBusyRetry(ctx, func() error {
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		now := time.Now().UnixMilli()
		var leaseRunID sql.NullInt64
		if err := tx.QueryRowContext(ctx, `select run_id from codex_inspection_leases where id = 1 and owner_id = ? and lease_expires_at_ms > ?`, ownerID, now).Scan(&leaseRunID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrLeaseLost
			}
			return err
		}
		if !leaseRunID.Valid || leaseRunID.Int64 != runID {
			return ErrLeaseLost
		}
		res, err := tx.ExecContext(ctx, `update codex_inspection_runs set status = ?, error = case when ? <> '' then ? else error end, updated_at_ms = ? where id = ? and status = ? and exists (
			select 1 from codex_inspection_leases
			where id = 1 and run_id = ? and owner_id = ? and lease_expires_at_ms > ?
		)`, model.CodexInspectionStatusCancelling, reason, reason, now, runID, model.CodexInspectionStatusRunning, runID, ownerID, now)
		if err != nil {
			return err
		}
		count, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			var status string
			if err := tx.QueryRowContext(ctx, `select status from codex_inspection_runs where id = ?`, runID).Scan(&status); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return sql.ErrNoRows
				}
				return err
			}
			if status == model.CodexInspectionStatusRunning {
				return ErrLeaseLost
			}
			changed = status == model.CodexInspectionStatusCancelling
		} else {
			changed = true
		}
		return tx.Commit()
	})
	return changed, err
}

func (r *repository) FinalizeRun(ctx context.Context, run model.CodexInspectionRun, ownerID string, finalLog *model.CodexInspectionLog) error {
	return withSQLiteBusyRetry(ctx, func() error {
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		effectiveStatus, effectiveError, err := finalizeRunTx(ctx, tx, run, ownerID)
		if err != nil {
			return err
		}
		if finalLog != nil {
			normalizedLog := normalizeFinalLifecycleLog(*finalLog, effectiveStatus, effectiveError)
			if _, err := insertLogTx(ctx, tx, normalizedLog); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `delete from codex_inspection_leases where id = 1 and run_id = ? and owner_id = ? and lease_expires_at_ms > ?`, run.ID, ownerID, time.Now().UnixMilli())
		if err != nil {
			return err
		}
		count, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrLeaseLost
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return nil
	})
}

// ForceFinalizeRun is used only after the normal lease-fenced finalization
// failed. It still requires the expired lease row to belong to the same
// run/owner, so a stale worker cannot finalize a row after another instance
// has released or reclaimed that lease. A lease belonging to another
// run/owner (or no lease at all) makes the operation fail with ErrLeaseLost.
// This closes the small window where a heartbeat outage would otherwise leave
// a completed worker's row permanently in running until a later restart.
func (r *repository) ForceFinalizeRun(ctx context.Context, run model.CodexInspectionRun, ownerID string, finalLog *model.CodexInspectionLog) error {
	if run.ID <= 0 || strings.TrimSpace(ownerID) == "" {
		return ErrLeaseLost
	}
	err := withSQLiteBusyRetry(ctx, func() error {
		return r.forceFinalizeRunOnce(ctx, run, ownerID, finalLog)
	})
	if err == nil || errors.Is(err, ErrLeaseLost) || finalLog == nil {
		return err
	}
	// A lifecycle log is useful but must never strand the terminal state. Retry
	// the same fenced transition without the optional log when its insert was
	// the failing write.
	return withSQLiteBusyRetry(ctx, func() error {
		return r.forceFinalizeRunOnce(ctx, run, ownerID, nil)
	})
}

func (r *repository) forceFinalizeRunOnce(ctx context.Context, run model.CodexInspectionRun, ownerID string, finalLog *model.CodexInspectionLog) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UnixMilli()
	run = prepareRun(run, now)
	if run.FinishedAtMS <= 0 {
		run.FinishedAtMS = now
	}
	run.Status = model.NormalizeCodexInspectionRunStatus(run.Status)
	if !isTerminalStatus(run.Status) {
		return ErrInvalidFinalStatus
	}
	res, err := tx.ExecContext(ctx, `update codex_inspection_runs set
		status = case when status = ? then ? else ? end, finished_at_ms = ?, total_files = ?, probe_set_count = ?, sampled_count = ?,
		disabled_count = ?, enabled_count = ?, delete_count = ?, disable_count = ?, enable_count = ?,
		reauth_count = ?, keep_count = ?, error = case when status = ? then coalesce(nullif(error, ''), ?) else ? end, settings_json = ?, updated_at_ms = ?
		where id = ? and status in (?, ?) and exists (
			select 1 from codex_inspection_leases where id = 1 and owner_id = ? and run_id = ?
		)`,
		model.CodexInspectionStatusCancelling, model.CodexInspectionStatusCancelled, run.Status,
		nullPositiveInt64(run.FinishedAtMS), run.TotalFiles, run.ProbeSetCount,
		run.SampledCount, run.DisabledCount, run.EnabledCount, run.DeleteCount, run.DisableCount, run.EnableCount,
		run.ReauthCount, run.KeepCount,
		model.CodexInspectionStatusCancelling, userCancelledFallbackReason, nullString(run.Error), run.SettingsJSON, run.UpdatedAtMS, run.ID,
		model.CodexInspectionStatusRunning, model.CodexInspectionStatusCancelling, ownerID, run.ID)
	if err != nil {
		return err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrLeaseLost
	}
	effectiveStatus, effectiveError, err := loadFinalRunStateTx(ctx, tx, run.ID)
	if err != nil {
		return err
	}
	if finalLog != nil {
		normalizedLog := normalizeFinalLifecycleLog(*finalLog, effectiveStatus, effectiveError)
		if _, err := insertLogTx(ctx, tx, normalizedLog); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `delete from codex_inspection_leases
		where id = 1 and owner_id = ? and run_id = ?`, ownerID, run.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *repository) GetActiveLease(ctx context.Context, nowMS int64) (model.CodexInspectionLease, bool, error) {
	if nowMS <= 0 {
		nowMS = time.Now().UnixMilli()
	}
	var lease model.CodexInspectionLease
	var runID sql.NullInt64
	if err := r.db.QueryRowContext(ctx, `select l.run_id, l.owner_id, l.heartbeat_at_ms, l.lease_expires_at_ms
		from codex_inspection_leases l
		join codex_inspection_runs r on r.id = l.run_id and r.status in (?, ?)
		where l.id = 1 and l.lease_expires_at_ms > ?`, model.CodexInspectionStatusRunning, model.CodexInspectionStatusCancelling, nowMS).Scan(&runID, &lease.OwnerID, &lease.HeartbeatAtMS, &lease.LeaseExpiresAtMS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.CodexInspectionLease{}, false, nil
		}
		return model.CodexInspectionLease{}, false, err
	}
	if !runID.Valid || runID.Int64 <= 0 {
		return model.CodexInspectionLease{}, false, nil
	}
	lease.RunID = runID.Int64
	return lease, true, nil
}

func (r *repository) RecoverStaleRuns(ctx context.Context, nowMS int64, reason string) ([]model.CodexInspectionRun, error) {
	if nowMS <= 0 {
		nowMS = time.Now().UnixMilli()
	}
	if strings.TrimSpace(reason) == "" {
		reason = "服务重启或任务租约过期，巡检已中断"
	}
	var recovered []model.CodexInspectionRun
	err := withSQLiteBusyRetry(ctx, func() error {
		recovered = recovered[:0]
		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		// An unbound lease is only an incomplete acquisition. It is never a
		// valid active task and must not block recovery or the next run after a
		// crash between lease creation and run binding.
		if _, err := tx.ExecContext(ctx, `delete from codex_inspection_leases where id = 1 and (run_id is null or run_id <= 0 or not exists (
				select 1 from codex_inspection_runs where id = codex_inspection_leases.run_id and status in (?, ?)
			))`, model.CodexInspectionStatusRunning, model.CodexInspectionStatusCancelling); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `select r.id, r.status, r.started_at_ms, coalesce(l.run_id, 0), coalesce(l.lease_expires_at_ms, 0)
			from codex_inspection_runs r left join codex_inspection_leases l on l.run_id = r.id
			where r.status in (?, ?)`, model.CodexInspectionStatusRunning, model.CodexInspectionStatusCancelling)
		if err != nil {
			return err
		}
		defer rows.Close()
		candidateIDs := make([]struct {
			id, started, leaseRunID, leaseExpiry int64
			status                               string
		}, 0)
		for rows.Next() {
			var item struct {
				id, started, leaseRunID, leaseExpiry int64
				status                               string
			}
			if err := rows.Scan(&item.id, &item.status, &item.started, &item.leaseRunID, &item.leaseExpiry); err != nil {
				return err
			}
			if item.leaseRunID == item.id && item.leaseExpiry > nowMS {
				continue
			}
			candidateIDs = append(candidateIDs, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, item := range candidateIDs {
			// Re-check the lease in the same state transition that marks the
			// run interrupted. A heartbeat that wins the race makes this update
			// affect zero rows; conversely, once the run is interrupted the
			// heartbeat fence prevents the old owner from extending its lease.
			statusResult, err := tx.ExecContext(ctx, `update codex_inspection_runs set status = ?, finished_at_ms = ?, error = ?, updated_at_ms = ? where id = ? and status in (?, ?) and not exists (
				select 1 from codex_inspection_leases
				where id = 1 and run_id = ? and lease_expires_at_ms > ?
			)`, reasonStatus(reason), nowMS, reason, nowMS, item.id, model.CodexInspectionStatusRunning, model.CodexInspectionStatusCancelling, item.id, nowMS)
			if err != nil {
				return err
			}
			statusRows, err := statusResult.RowsAffected()
			if err != nil {
				return err
			}
			if statusRows != 1 {
				continue
			}
			if item.leaseRunID == item.id {
				if _, err := tx.ExecContext(ctx, `delete from codex_inspection_leases where id = 1 and run_id = ? and lease_expires_at_ms <= ?`, item.id, nowMS); err != nil {
					return err
				}
			}
			recovered = append(recovered, model.CodexInspectionRun{ID: item.id, Status: reasonStatus(reason), StartedAtMS: item.started, FinishedAtMS: nowMS, Error: reason, UpdatedAtMS: nowMS})
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return recovered, err
	}
	var logErrs []error
	logCtx, cancelLogs := context.WithTimeout(context.WithoutCancel(ctx), recoveryLogWriteTimeout)
	defer cancelLogs()
	for index, item := range recovered {
		if logCtx.Err() != nil {
			logErrs = append(logErrs, fmt.Errorf("%d recovery logs not written: %w", len(recovered)-index, logCtx.Err()))
			break
		}
		_, logErr := r.InsertLog(logCtx, model.CodexInspectionLog{
			RunID:       item.ID,
			Level:       "warning",
			Message:     reason,
			Detail:      map[string]any{"reason": "startup_recovery"},
			CreatedAtMS: item.FinishedAtMS,
		})
		if logErr != nil {
			logErrs = append(logErrs, fmt.Errorf("run %d: %w", item.ID, logErr))
		}
	}
	if len(logErrs) > 0 {
		return recovered, fmt.Errorf("write codex inspection recovery logs: %w", errors.Join(logErrs...))
	}
	return recovered, nil
}

func reasonStatus(_ string) string { return model.CodexInspectionStatusInterrupted }

func finalizeRunTx(ctx context.Context, tx *sql.Tx, run model.CodexInspectionRun, ownerID string) (string, string, error) {
	now := time.Now().UnixMilli()
	run = prepareRun(run, now)
	if run.FinishedAtMS <= 0 {
		run.FinishedAtMS = now
	}
	run.Status = model.NormalizeCodexInspectionRunStatus(run.Status)
	if !isTerminalStatus(run.Status) {
		return "", "", ErrInvalidFinalStatus
	}
	res, err := tx.ExecContext(ctx, `update codex_inspection_runs set
			finished_at_ms = ?, total_files = ?, probe_set_count = ?, sampled_count = ?,
			disabled_count = ?, enabled_count = ?, delete_count = ?, disable_count = ?, enable_count = ?,
			reauth_count = ?, keep_count = ?,
			status = case when status = ? then ? else ? end,
			error = case when status = ? then coalesce(nullif(error, ''), ?) else ? end,
			settings_json = ?, updated_at_ms = ?
			where id = ? and status in (?, ?) and exists (
			select 1 from codex_inspection_leases where id = 1 and run_id = ? and owner_id = ? and lease_expires_at_ms > ?
			)`,
		nullPositiveInt64(run.FinishedAtMS), run.TotalFiles, run.ProbeSetCount,
		run.SampledCount, run.DisabledCount, run.EnabledCount, run.DeleteCount, run.DisableCount, run.EnableCount,
		run.ReauthCount, run.KeepCount,
		model.CodexInspectionStatusCancelling, model.CodexInspectionStatusCancelled, run.Status,
		model.CodexInspectionStatusCancelling, userCancelledFallbackReason, nullString(run.Error),
		run.SettingsJSON, run.UpdatedAtMS, run.ID,
		model.CodexInspectionStatusRunning, model.CodexInspectionStatusCancelling, run.ID, ownerID, now)
	if err != nil {
		return "", "", err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return "", "", err
	}
	if changed != 1 {
		return "", "", ErrLeaseLost
	}
	return loadFinalRunStateTx(ctx, tx, run.ID)
}

func loadFinalRunStateTx(ctx context.Context, tx *sql.Tx, runID int64) (string, string, error) {
	var status string
	var errorText sql.NullString
	if err := tx.QueryRowContext(ctx, `select status, error from codex_inspection_runs where id = ?`, runID).Scan(&status, &errorText); err != nil {
		return "", "", err
	}
	return status, errorText.String, nil
}

func normalizeFinalLifecycleLog(entry model.CodexInspectionLog, status, errorText string) model.CodexInspectionLog {
	detail := make(map[string]any)
	if existing, ok := entry.Detail.(map[string]any); ok {
		for key, value := range existing {
			detail[key] = value
		}
	} else if entry.DetailJSON != "" {
		_ = json.Unmarshal([]byte(entry.DetailJSON), &detail)
	}
	detail["status"] = status
	if errorText != "" {
		detail["error"] = errorText
	}
	entry.Detail = detail
	entry.DetailJSON = ""

	switch status {
	case model.CodexInspectionStatusCancelled:
		entry.Level = "warning"
		entry.Message = "凭证健康巡检已取消"
		detail["reason"] = "user_cancel"
	case model.CodexInspectionStatusInterrupted:
		entry.Level = "warning"
		entry.Message = "凭证健康巡检已中断"
		if reason, _ := detail["reason"].(string); strings.TrimSpace(reason) == "" || reason == "none" {
			detail["reason"] = "interrupted"
		}
	case model.CodexInspectionStatusFailed:
		entry.Level = "error"
	}
	return entry
}

func isTerminalStatus(status string) bool {
	switch status {
	case model.CodexInspectionStatusCompleted,
		model.CodexInspectionStatusFailed,
		model.CodexInspectionStatusCancelled,
		model.CodexInspectionStatusInterrupted:
		return true
	default:
		return false
	}
}

func insertLogTx(ctx context.Context, tx *sql.Tx, entry model.CodexInspectionLog) (model.CodexInspectionLog, error) {
	if entry.CreatedAtMS <= 0 {
		entry.CreatedAtMS = time.Now().UnixMilli()
	}
	if entry.DetailJSON == "" && entry.Detail != nil {
		if data, err := json.Marshal(entry.Detail); err == nil {
			entry.DetailJSON = string(data)
		}
	}
	if err := tx.QueryRowContext(ctx, `insert into codex_inspection_logs(run_id, level, message, detail_json, created_at_ms) values (?, ?, ?, ?, ?) returning id`, entry.RunID, entry.Level, entry.Message, nullString(entry.DetailJSON), entry.CreatedAtMS).Scan(&entry.ID); err != nil {
		return model.CodexInspectionLog{}, err
	}
	return entry, nil
}
