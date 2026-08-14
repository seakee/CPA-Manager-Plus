package usagemonitoring

import (
	"context"
	"database/sql"
)

func (r *repository) LoadAggregate(ctx context.Context, filter AnalyticsFilter) (Aggregate, State, bool, error) {
	if !SupportsEventProjectionFilter(filter) {
		return Aggregate{}, State{}, false, nil
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Aggregate{}, State{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	state, available, projectionComplete, err := projectionReadState(ctx, tx)
	if err != nil || !available {
		return Aggregate{}, state, available, err
	}
	statsState, revision, dailyAvailable, err := statsReadState(ctx, tx)
	if err != nil {
		return Aggregate{}, state, false, err
	}
	if dailyAvailable && SupportsStatsFilter(filter) {
		aggregate, err := loadDailyAggregate(
			ctx,
			tx,
			statsState,
			state.CoverageEventID,
			projectionComplete,
			revision,
			filter,
		)
		if err != nil {
			return Aggregate{}, state, false, err
		}
		if err := tx.Commit(); err != nil {
			return Aggregate{}, state, false, err
		}
		return aggregate, state, true, nil
	}

	var accumulator dailyAggregateAccumulator
	if err := mergeProjectedAggregate(
		ctx,
		tx,
		state.CoverageEventID,
		projectionComplete,
		filter,
		0,
		false,
		&accumulator,
	); err != nil {
		return Aggregate{}, state, false, err
	}
	if err := tx.Commit(); err != nil {
		return Aggregate{}, state, false, err
	}
	return accumulator.result(), state, true, nil
}

func (r *repository) LoadModelStats(ctx context.Context, filter AnalyticsFilter) ([]ModelStat, State, bool, error) {
	if !SupportsEventProjectionFilter(filter) {
		return nil, State{}, false, nil
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, State{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	state, available, projectionComplete, err := projectionReadState(ctx, tx)
	if err != nil || !available {
		return nil, state, available, err
	}
	statsState, revision, dailyAvailable, err := statsReadState(ctx, tx)
	if err != nil {
		return nil, state, false, err
	}
	if dailyAvailable && SupportsStatsFilter(filter) {
		stats, err := loadDailyModelStats(
			ctx,
			tx,
			statsState,
			state.CoverageEventID,
			projectionComplete,
			revision,
			filter,
		)
		if err != nil {
			return nil, state, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, state, false, err
		}
		return stats, state, true, nil
	}

	grouped := make(map[dailyModelStatKey]*ModelStat)
	if err := mergeProjectedModelStats(
		ctx,
		tx,
		state.CoverageEventID,
		projectionComplete,
		filter,
		0,
		false,
		grouped,
	); err != nil {
		return nil, state, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, state, false, err
	}
	return sortedDailyModelStats(grouped), state, true, nil
}

func projectionReadState(ctx context.Context, tx *sql.Tx) (State, bool, bool, error) {
	state, err := stateQuery(ctx, tx, ProjectionRollupName)
	if err != nil {
		return State{}, false, false, err
	}
	if state.SchemaVersion != SchemaVersion {
		return state, false, false, nil
	}
	latestID, err := latestEventID(ctx, tx)
	if err != nil {
		return State{}, false, false, err
	}
	return state, true, state.CoverageEventID >= latestID, nil
}
