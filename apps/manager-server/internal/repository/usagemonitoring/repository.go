package usagemonitoring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageevent"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageprojection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	StatsRollupName      = "stats_v1"
	MetadataRollupName   = "metadata_v1"
	ProjectionRollupName = "projection_v1"
	SchemaVersion        = 1
	defaultBatchLimit    = 1000
)

var ErrUnsupportedSchema = errors.New("unsupported usage monitoring rollup schema")

type Repository interface {
	CatchUpProjection(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error)
	CatchUpStats(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error)
	CatchUpMetadata(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error)
	CatchUpCodexLegacyIdentityEvidence(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error)
	RecordFailure(ctx context.Context, rollupName string, rollupErr error, nowMS int64) error
	State(ctx context.Context, rollupName string) (State, error)
	LoadAggregate(ctx context.Context, filter AnalyticsFilter) (Aggregate, State, bool, error)
	LoadModelStats(ctx context.Context, filter AnalyticsFilter) ([]ModelStat, State, bool, error)
	LoadAccountStats(ctx context.Context, filter AnalyticsFilter) ([]AccountModelStat, State, bool, error)
	LoadAccountWindowStats(ctx context.Context, windows []AccountWindowUsageQuery) ([]AccountWindowModelStat, State, bool, error)
	LoadAPIKeyStats(ctx context.Context, filter AnalyticsFilter) ([]APIKeyModelStat, State, bool, error)
	LoadFilterOptions(ctx context.Context, filter AnalyticsFilter) (FilterOptionValues, State, bool, error)
	LoadFilterSelectors(ctx context.Context, filter AnalyticsFilter) (FilterSelectorValues, State, bool, error)
	LoadEventsCount(ctx context.Context, filter AnalyticsFilter) (int64, State, bool, error)
	LoadEventsPage(ctx context.Context, filter AnalyticsFilter, beforeMS, beforeID int64, limit int) (EventsPage, State, bool, error)
	LoadHeaderSnapshots(ctx context.Context, sinceMS int64, limit int) ([]HeaderSnapshot, State, bool, error)
}

type State struct {
	RollupName          string
	SchemaVersion       int
	StructureRevision   string
	Status              string
	BackfillLastEventID int64
	CoverageEventID     int64
	TargetEventID       int64
	ProcessedEvents     int64
	LastRunStartedAtMS  sql.NullInt64
	UpdatedAtMS         int64
	FinishedAtMS        sql.NullInt64
	LastError           string
}

type CatchUpResult struct {
	Processed       int
	LastEventID     int64
	CoverageEventID int64
	TargetEventID   int64
	Pending         bool
	Rebuilt         bool
	ContinueSoon    bool
}

type repository struct {
	db          *sql.DB
	catchUpGate chan struct{}
}

func New(db *sql.DB) Repository {
	return &repository{db: db, catchUpGate: make(chan struct{}, 1)}
}

func (r *repository) CatchUpProjection(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error) {
	return r.catchUp(ctx, ProjectionRollupName, limit, nowMS, func(ctx context.Context, tx *sql.Tx, _ string, afterID, throughID, updatedAtMS int64) error {
		return usageprojection.UpsertEventRange(ctx, tx, afterID, throughID, updatedAtMS)
	})
}

func (r *repository) CatchUpStats(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error) {
	return r.catchUp(ctx, StatsRollupName, limit, nowMS, func(ctx context.Context, tx *sql.Tx, revision string, afterID, throughID, updatedAtMS int64) error {
		if err := upsertAccountDailyBatch(ctx, tx, revision, afterID, throughID, updatedAtMS); err != nil {
			return err
		}
		return upsertAPIKeyDailyBatch(ctx, tx, revision, afterID, throughID, updatedAtMS)
	})
}

func (r *repository) CatchUpMetadata(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error) {
	return r.catchUp(ctx, MetadataRollupName, limit, nowMS, func(ctx context.Context, tx *sql.Tx, _ string, afterID, throughID, updatedAtMS int64) error {
		if err := upsertSelectorDailyBatch(ctx, tx, afterID, throughID, updatedAtMS); err != nil {
			return err
		}
		return usageprojection.UpsertHeaderRange(ctx, tx, afterID, throughID, updatedAtMS)
	})
}

func (r *repository) CatchUpCodexLegacyIdentityEvidence(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error) {
	if limit <= 0 || limit > defaultBatchLimit {
		limit = defaultBatchLimit
	}
	return r.catchUp(ctx, usageevent.CodexLegacyIdentityRollupName, limit, nowMS, func(ctx context.Context, tx *sql.Tx, revision string, afterID, throughID, _ int64) error {
		return usageevent.UpsertCodexLegacyIdentityEvidenceRange(ctx, tx, revision, afterID, throughID)
	})
}

type batchUpserter func(context.Context, *sql.Tx, string, int64, int64, int64) error

func (r *repository) catchUp(
	ctx context.Context,
	rollupName string,
	limit int,
	nowMS int64,
	upsertBatch batchUpserter,
) (CatchUpResult, error) {
	if limit <= 0 {
		limit = defaultBatchLimit
	}
	if nowMS <= 0 {
		return CatchUpResult{}, errors.New("nowMS must be greater than 0")
	}
	if err := r.acquireCatchUp(ctx); err != nil {
		return CatchUpResult{}, err
	}
	defer r.releaseCatchUp()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return CatchUpResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `update usage_monitoring_rollup_state set
		last_run_started_at_ms = ? where rollup_name = ?`, nowMS, rollupName); err != nil {
		return CatchUpResult{}, err
	}

	state, err := stateQuery(ctx, tx, rollupName)
	if err != nil {
		return CatchUpResult{}, err
	}
	expectedSchema := expectedSchemaVersion(rollupName)
	if state.SchemaVersion != expectedSchema {
		return CatchUpResult{}, fmt.Errorf("%w: %s got %d, want %d", ErrUnsupportedSchema, rollupName, state.SchemaVersion, expectedSchema)
	}
	latestID, err := latestEventID(ctx, tx)
	if err != nil {
		return CatchUpResult{}, err
	}

	revision := state.StructureRevision
	rebuilt := state.CoverageEventID < state.TargetEventID &&
		(state.Status == "pending" || state.Status == "rebuilding" || state.Status == "clearing")
	if rollupName == StatsRollupName {
		revision, err = currentStructureRevision(ctx, tx)
	} else if rollupName == ProjectionRollupName {
		revision = usageidentity.MonitoringProjectionStructureRevision()
	} else if rollupName == usageevent.CodexLegacyIdentityRollupName {
		revision = usageevent.CodexLegacyIdentityEvidenceRevision
	} else {
		revision = usageidentity.ModelFormatVersion
	}
	if err != nil {
		return CatchUpResult{}, err
	}
	if state.StructureRevision != revision {
		if err := resetForRevision(ctx, tx, rollupName, revision, latestID, nowMS); err != nil {
			return CatchUpResult{}, err
		}
		state = State{
			RollupName:        rollupName,
			SchemaVersion:     expectedSchema,
			StructureRevision: revision,
			Status:            revisionResetStatus(rollupName),
			TargetEventID:     latestID,
			UpdatedAtMS:       nowMS,
		}
		rebuilt = true
	}
	if state.Status == "clearing" {
		var pending bool
		var err error
		if rollupName == usageevent.CodexLegacyIdentityRollupName {
			pending, err = usageevent.ClearCodexLegacyIdentityEvidenceBatch(ctx, tx, limit)
		} else {
			pending, err = clearStatsRevisionRowsBatch(ctx, tx, revision, limit)
		}
		if err != nil {
			return CatchUpResult{}, err
		}
		if pending {
			if err := tx.Commit(); err != nil {
				return CatchUpResult{}, err
			}
			return CatchUpResult{
				TargetEventID: state.TargetEventID,
				Pending:       true,
				Rebuilt:       true,
				ContinueSoon:  true,
			}, nil
		}
		if _, err := tx.ExecContext(ctx, `update usage_monitoring_rollup_state set
			status = 'rebuilding', updated_at_ms = ?, last_error = null
			where rollup_name = ? and structure_revision = ?`,
			nowMS, rollupName, revision); err != nil {
			return CatchUpResult{}, err
		}
		state.Status = "rebuilding"
	}

	targetID := state.TargetEventID
	if targetID < state.CoverageEventID {
		targetID = state.CoverageEventID
	}
	if !rebuilt && state.CoverageEventID >= targetID && latestID > state.CoverageEventID {
		targetID = latestID
		if _, err := tx.ExecContext(ctx, `update usage_monitoring_rollup_state set
			status = 'catching_up', target_event_id = ?, finished_at_ms = null
			where rollup_name = ?`, targetID, rollupName); err != nil {
			return CatchUpResult{}, err
		}
	}

	ids, err := eventIDsThrough(ctx, tx, state.CoverageEventID, targetID, limit)
	if err != nil {
		return CatchUpResult{}, err
	}
	if len(ids) == 0 {
		coverageID := state.CoverageEventID
		if targetID > coverageID {
			coverageID = targetID
		}
		pending := latestID > coverageID
		status := "ready"
		finishedAt := any(nowMS)
		if pending {
			status = "catching_up"
			finishedAt = nil
		}
		if _, err := tx.ExecContext(ctx, `update usage_monitoring_rollup_state set
			status = ?, backfill_last_event_id = ?, coverage_event_id = ?,
			target_event_id = ?, last_run_started_at_ms = ?, updated_at_ms = ?,
			finished_at_ms = ?, last_error = null
			where rollup_name = ?`,
			status, coverageID, coverageID, targetID, nowMS, nowMS, finishedAt, rollupName,
		); err != nil {
			return CatchUpResult{}, err
		}
		if err := setSearchIndexReady(ctx, tx, rollupName, !pending, nowMS); err != nil {
			return CatchUpResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return CatchUpResult{}, err
		}
		return CatchUpResult{
			LastEventID:     coverageID,
			CoverageEventID: coverageID,
			TargetEventID:   targetID,
			Pending:         pending,
			Rebuilt:         rebuilt,
		}, nil
	}

	lastEventID := ids[len(ids)-1]
	if err := upsertBatch(ctx, tx, revision, state.CoverageEventID, lastEventID, nowMS); err != nil {
		return CatchUpResult{}, err
	}
	pending := lastEventID < targetID || latestID > lastEventID
	status := "ready"
	finishedAt := any(nowMS)
	if lastEventID < targetID && rebuilt {
		status = "rebuilding"
		finishedAt = nil
	} else if pending {
		status = "catching_up"
		finishedAt = nil
	}
	if _, err := tx.ExecContext(ctx, `update usage_monitoring_rollup_state set
		status = ?, backfill_last_event_id = ?, coverage_event_id = ?,
		target_event_id = ?, processed_events = processed_events + ?,
		last_run_started_at_ms = ?, updated_at_ms = ?, finished_at_ms = ?,
		last_error = null where rollup_name = ?`,
		status, lastEventID, lastEventID, targetID, len(ids), nowMS, nowMS, finishedAt, rollupName,
	); err != nil {
		return CatchUpResult{}, err
	}
	if err := setSearchIndexReady(ctx, tx, rollupName, !pending, nowMS); err != nil {
		return CatchUpResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CatchUpResult{}, err
	}
	return CatchUpResult{
		Processed:       len(ids),
		LastEventID:     lastEventID,
		CoverageEventID: lastEventID,
		TargetEventID:   targetID,
		Pending:         pending,
		Rebuilt:         rebuilt,
	}, nil
}

func (r *repository) RecordFailure(ctx context.Context, rollupName string, rollupErr error, nowMS int64) error {
	if rollupErr == nil || nowMS <= 0 {
		return nil
	}
	if rollupName != StatsRollupName && rollupName != MetadataRollupName && rollupName != ProjectionRollupName && rollupName != usageevent.CodexLegacyIdentityRollupName {
		return fmt.Errorf("unknown usage monitoring rollup %q", rollupName)
	}
	_, err := r.db.ExecContext(ctx, `update usage_monitoring_rollup_state set
		status = case
			when status in ('clearing', 'rebuilding') then status
			when status = 'pending' and target_event_id > coverage_event_id then status
			else 'failed'
		end,
		updated_at_ms = ?, finished_at_ms = ?, last_error = ?
		where rollup_name = ?`, nowMS, nowMS, rollupErr.Error(), rollupName)
	return err
}

func (r *repository) State(ctx context.Context, rollupName string) (State, error) {
	return stateQuery(ctx, r.db, rollupName)
}

func (r *repository) acquireCatchUp(ctx context.Context) error {
	select {
	case r.catchUpGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *repository) releaseCatchUp() {
	select {
	case <-r.catchUpGate:
	default:
	}
}

type stateQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func stateQuery(ctx context.Context, db stateQuerier, rollupName string) (State, error) {
	var state State
	var lastError sql.NullString
	err := db.QueryRowContext(ctx, `select
		rollup_name, schema_version, structure_revision, status,
		backfill_last_event_id, coverage_event_id, target_event_id,
		processed_events, last_run_started_at_ms, updated_at_ms,
		finished_at_ms, last_error
		from usage_monitoring_rollup_state where rollup_name = ?`, rollupName).Scan(
		&state.RollupName,
		&state.SchemaVersion,
		&state.StructureRevision,
		&state.Status,
		&state.BackfillLastEventID,
		&state.CoverageEventID,
		&state.TargetEventID,
		&state.ProcessedEvents,
		&state.LastRunStartedAtMS,
		&state.UpdatedAtMS,
		&state.FinishedAtMS,
		&lastError,
	)
	if err != nil {
		return State{}, err
	}
	state.LastError = lastError.String
	return state, nil
}

func latestEventID(ctx context.Context, tx *sql.Tx) (int64, error) {
	var id int64
	if err := tx.QueryRowContext(ctx, `select coalesce(max(id), 0) from usage_events`).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func eventIDsThrough(ctx context.Context, tx *sql.Tx, afterID, throughID int64, limit int) ([]int64, error) {
	if throughID <= afterID {
		return []int64{}, nil
	}
	rows, err := tx.QueryContext(ctx, `select id from usage_events
		where id > ? and id <= ? order by id limit ?`, afterID, throughID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func resetForRevision(ctx context.Context, tx *sql.Tx, rollupName, revision string, latestID, nowMS int64) error {
	if _, err := tx.ExecContext(ctx, `update usage_monitoring_rollup_state set
		structure_revision = ?, status = ?,
		backfill_last_event_id = 0, coverage_event_id = 0,
		target_event_id = ?, processed_events = 0,
		last_run_started_at_ms = ?, updated_at_ms = ?, finished_at_ms = null,
		last_error = null where rollup_name = ?`,
		revision, revisionResetStatus(rollupName), latestID, nowMS, nowMS, rollupName,
	); err != nil {
		return err
	}
	if rollupName != ProjectionRollupName {
		return nil
	}
	return setSearchIndexReady(ctx, tx, rollupName, false, nowMS)
}

func revisionResetStatus(rollupName string) string {
	if rollupName == StatsRollupName || rollupName == usageevent.CodexLegacyIdentityRollupName {
		return "clearing"
	}
	return "rebuilding"
}

func clearStatsRevisionRowsBatch(ctx context.Context, tx *sql.Tx, revision string, limit int) (bool, error) {
	remaining := limit
	for _, tableName := range []string{
		"usage_monitoring_account_daily_rollups_v1",
		"usage_monitoring_api_key_daily_rollups_v1",
	} {
		if remaining > 0 {
			result, err := tx.ExecContext(ctx, `delete from `+tableName+` where rowid in (
				select rowid from `+tableName+` where structure_revision = ? limit ?
			)`, revision, remaining)
			if err != nil {
				return false, fmt.Errorf("clear stats revision rows %s: %w", tableName, err)
			}
			deleted, err := result.RowsAffected()
			if err != nil {
				return false, fmt.Errorf("count cleared stats revision rows %s: %w", tableName, err)
			}
			remaining -= int(deleted)
		}
		var pending int
		if err := tx.QueryRowContext(ctx, `select exists(
			select 1 from `+tableName+` where structure_revision = ? limit 1
		)`, revision).Scan(&pending); err != nil {
			return false, fmt.Errorf("inspect remaining stats revision rows %s: %w", tableName, err)
		}
		if pending != 0 {
			return true, nil
		}
	}
	return false, nil
}

func setSearchIndexReady(ctx context.Context, tx *sql.Tx, rollupName string, ready bool, nowMS int64) error {
	if rollupName != ProjectionRollupName {
		return nil
	}
	readyValue := 0
	if ready {
		readyValue = 1
	}
	_, err := tx.ExecContext(ctx, `update usage_monitoring_search_index_state set
		ready = ?, updated_at_ms = ? where id = 1`, readyValue, nowMS)
	return err
}

func expectedSchemaVersion(rollupName string) int {
	if rollupName == usageevent.CodexLegacyIdentityRollupName {
		return usageevent.CodexLegacyIdentityEvidenceSchemaVersion
	}
	return SchemaVersion
}
