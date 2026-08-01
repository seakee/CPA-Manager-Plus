package usageevent

import (
	"context"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

const defaultModelUsageLimit = 50000

func (r *repository) ModelUsageSummary(ctx context.Context, limit int) (model.ModelUsageSummary, error) {
	if limit <= 0 {
		limit = defaultModelUsageLimit
	}

	rows, err := r.db.QueryContext(ctx, `with recent as (
		select model, coalesce(resolved_model, '') as resolved_model
		from usage_events
		order by timestamp_ms desc, id desc
		limit ?
	), model_counts as (
		select model, count(*) as requested_calls, 0 as resolved_calls
		from recent
		where model <> ''
		group by model
		union all
		select resolved_model as model, 0 as requested_calls, count(*) as resolved_calls
		from recent
		where resolved_model <> '' and resolved_model <> model
		group by resolved_model
	), aggregated as (
		select
			model,
			sum(requested_calls) as requested_calls,
			sum(resolved_calls) as resolved_calls
		from model_counts
		group by model
	)
	select model, requested_calls, resolved_calls
	from aggregated
	order by requested_calls + resolved_calls desc, model asc`, limit)
	if err != nil {
		return model.ModelUsageSummary{}, err
	}
	defer rows.Close()

	models := make([]model.ModelUsageStat, 0)
	for rows.Next() {
		var stat model.ModelUsageStat
		if err := rows.Scan(&stat.Model, &stat.RequestedCalls, &stat.ResolvedCalls); err != nil {
			return model.ModelUsageSummary{}, err
		}
		stat.Calls = stat.RequestedCalls + stat.ResolvedCalls
		models = append(models, stat)
	}
	if err := rows.Err(); err != nil {
		return model.ModelUsageSummary{}, err
	}

	var totalEvents int64
	if err := r.db.QueryRowContext(ctx, `select count(*) from usage_events`).Scan(&totalEvents); err != nil {
		return model.ModelUsageSummary{}, err
	}

	sampledEvents := totalEvents
	if sampledEvents > int64(limit) {
		sampledEvents = int64(limit)
	}
	return model.ModelUsageSummary{
		SampledEvents: sampledEvents,
		TotalEvents:   totalEvents,
		Truncated:     sampledEvents < totalEvents,
		Models:        models,
	}, nil
}
