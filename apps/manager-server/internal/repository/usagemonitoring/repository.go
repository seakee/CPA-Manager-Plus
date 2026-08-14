package usagemonitoring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageprojection"
)

const (
	StatsRollupName      = "stats_v1"
	MetadataRollupName   = "metadata_v1"
	ProjectionRollupName = "projection_v1"
	SchemaVersion        = 2
	defaultBatchLimit    = 1000
)

var ErrUnsupportedSchema = errors.New("unsupported usage monitoring rollup schema")

type Repository interface {
	CatchUpProjection(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error)
	CatchUpStats(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error)
	CatchUpMetadata(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error)
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
}

type repository struct {
	db          *sql.DB
	catchUpGate chan struct{}
}

func New(db *sql.DB) Repository {
	return &repository{db: db, catchUpGate: make(chan struct{}, 1)}
}

func (r *repository) CatchUpProjection(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error) {
	return r.catchUp(ctx, ProjectionRollupName, limit, nowMS, false, func(ctx context.Context, tx *sql.Tx, _ string, afterID, throughID, updatedAtMS int64) error {
		return usageprojection.UpsertEventRange(ctx, tx, afterID, throughID, updatedAtMS)
	})
}

func (r *repository) CatchUpStats(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error) {
	return r.catchUp(ctx, StatsRollupName, limit, nowMS, true, func(ctx context.Context, tx *sql.Tx, revision string, afterID, throughID, updatedAtMS int64) error {
		if err := upsertAccountDailyBatch(ctx, tx, revision, afterID, throughID, updatedAtMS); err != nil {
			return err
		}
		return upsertAPIKeyDailyBatch(ctx, tx, revision, afterID, throughID, updatedAtMS)
	})
}

func (r *repository) CatchUpMetadata(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error) {
	return r.catchUp(ctx, MetadataRollupName, limit, nowMS, false, func(ctx context.Context, tx *sql.Tx, _ string, afterID, throughID, updatedAtMS int64) error {
		if err := upsertSelectorDailyBatch(ctx, tx, afterID, throughID, updatedAtMS); err != nil {
			return err
		}
		return usageprojection.UpsertHeaderRange(ctx, tx, afterID, throughID, updatedAtMS)
	})
}

type batchUpserter func(context.Context, *sql.Tx, string, int64, int64, int64) error

func (r *repository) catchUp(
	ctx context.Context,
	rollupName string,
	limit int,
	nowMS int64,
	pricingAware bool,
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
	if state.SchemaVersion != SchemaVersion {
		return CatchUpResult{}, fmt.Errorf("%w: %s got %d, want %d", ErrUnsupportedSchema, rollupName, state.SchemaVersion, SchemaVersion)
	}
	latestID, err := latestEventID(ctx, tx)
	if err != nil {
		return CatchUpResult{}, err
	}

	revision := state.StructureRevision
	rebuilt := false
	if pricingAware {
		revision, err = currentStructureRevision(ctx, tx)
		if err != nil {
			return CatchUpResult{}, err
		}
		if state.StructureRevision != revision {
			if err := resetStatsForRevision(ctx, tx, revision, latestID, nowMS); err != nil {
				return CatchUpResult{}, err
			}
			state = State{
				RollupName:        StatsRollupName,
				SchemaVersion:     SchemaVersion,
				StructureRevision: revision,
				Status:            "rebuilding",
				TargetEventID:     latestID,
				UpdatedAtMS:       nowMS,
			}
			rebuilt = true
		}
	}

	targetID := state.TargetEventID
	if targetID < state.CoverageEventID {
		targetID = state.CoverageEventID
	}
	if state.CoverageEventID >= targetID && latestID > state.CoverageEventID {
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
	if pending {
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
	if rollupName != StatsRollupName && rollupName != MetadataRollupName && rollupName != ProjectionRollupName {
		return fmt.Errorf("unknown usage monitoring rollup %q", rollupName)
	}
	_, err := r.db.ExecContext(ctx, `update usage_monitoring_rollup_state set
		status = 'failed', updated_at_ms = ?, finished_at_ms = ?, last_error = ?
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

func resetStatsForRevision(ctx context.Context, tx *sql.Tx, revision string, latestID, nowMS int64) error {
	for _, statement := range []string{
		`delete from usage_monitoring_account_daily_rollups_v1`,
		`delete from usage_monitoring_api_key_daily_rollups_v1`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `update usage_monitoring_rollup_state set
		structure_revision = ?, status = 'rebuilding',
		backfill_last_event_id = 0, coverage_event_id = 0,
		target_event_id = ?, processed_events = 0,
		last_run_started_at_ms = ?, updated_at_ms = ?, finished_at_ms = null,
		last_error = null where rollup_name = ?`,
		revision, latestID, nowMS, nowMS, StatsRollupName,
	)
	return err
}
