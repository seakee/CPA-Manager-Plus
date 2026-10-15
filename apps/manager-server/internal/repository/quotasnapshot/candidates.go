package quotasnapshot

import "context"

// CandidateIndexName identifies the managed index used for bounded quota
// evidence reads. Populated databases prepare it through cleanup-derived.
const CandidateIndexName = "idx_quota_snapshots_candidates"

const CandidateIndexSQL = `create index if not exists ` + CandidateIndexName + ` on account_quota_snapshots(
	account_key, provider,
	coalesce(cast(logical_window_id as text), 'legacy:' || provider_window_id || char(0) || model_scope_kind || char(0) || coalesce(model_scope_key, '') || char(0) || coalesce(model_ids_json, '')),
	source, observed_at_ms desc, id desc, observation_id, logical_window_id
) where (observation_id is null or logical_window_id is not null)`

// Seek between window/source partitions, then fetch only their newest eligible
// evidence. A global recent-row limit could hide a quiet window behind a busy
// one; ranking all history would make every usage insert pay for that history.
//
// The two recursive seeks are deliberately separate: SQLite can use the
// expression index for equality/source-range and partition-range predicates,
// but cannot seek that index for a row-value comparison of the expression.
const boundedCandidateCTE = `with recursive partition_heads(id) as (
	select id from (
		select id from account_quota_snapshots
		where account_key = ?1 and provider = ?2
			and (observation_id is null or logical_window_id is not null)
			and not (instr(coalesce(account_key, ''), ':codex-account:') > 0)
		order by coalesce(cast(logical_window_id as text), 'legacy:' || provider_window_id || char(0) || model_scope_kind || char(0) || coalesce(model_scope_key, '') || char(0) || coalesce(model_ids_json, '')), source
		limit 1
	)
	union all
	select coalesce(
		(select id from account_quota_snapshots
			where account_key = ?1 and provider = ?2
				and (observation_id is null or logical_window_id is not null)
				and coalesce(cast(logical_window_id as text), 'legacy:' || provider_window_id || char(0) || model_scope_kind || char(0) || coalesce(model_scope_key, '') || char(0) || coalesce(model_ids_json, '')) = (
					select coalesce(cast(logical_window_id as text), 'legacy:' || provider_window_id || char(0) || model_scope_kind || char(0) || coalesce(model_scope_key, '') || char(0) || coalesce(model_ids_json, '')) from account_quota_snapshots where id = partition_heads.id
				)
				and source > (select source from account_quota_snapshots where id = partition_heads.id)
			order by source limit 1),
		(select id from account_quota_snapshots
			where account_key = ?1 and provider = ?2
				and (observation_id is null or logical_window_id is not null)
				and coalesce(cast(logical_window_id as text), 'legacy:' || provider_window_id || char(0) || model_scope_kind || char(0) || coalesce(model_scope_key, '') || char(0) || coalesce(model_ids_json, '')) > (
					select coalesce(cast(logical_window_id as text), 'legacy:' || provider_window_id || char(0) || model_scope_kind || char(0) || coalesce(model_scope_key, '') || char(0) || coalesce(model_ids_json, '')) from account_quota_snapshots where id = partition_heads.id
				)
			order by coalesce(cast(logical_window_id as text), 'legacy:' || provider_window_id || char(0) || model_scope_kind || char(0) || coalesce(model_scope_key, '') || char(0) || coalesce(model_ids_json, '')), source limit 1)
	)
	from partition_heads where id is not null
), bounded_ids as materialized (
	select candidate.id,
		row_number() over (
			partition by head.id
			order by candidate.observed_at_ms desc, candidate.id desc
		) as source_rank
	from partition_heads
	join account_quota_snapshots head on head.id = partition_heads.id
	join account_quota_snapshots candidate on candidate.id in (
		select id from account_quota_snapshots
		where account_key = ?1 and provider = ?2
			and (observation_id is null or logical_window_id is not null)
			and coalesce(cast(logical_window_id as text), 'legacy:' || provider_window_id || char(0) || model_scope_kind || char(0) || coalesce(model_scope_key, '') || char(0) || coalesce(model_ids_json, '')) = coalesce(cast(head.logical_window_id as text), 'legacy:' || head.provider_window_id || char(0) || head.model_scope_kind || char(0) || coalesce(head.model_scope_key, '') || char(0) || coalesce(head.model_ids_json, ''))
			and source = head.source
			and (observation_id is null or exists (
				select 1 from account_quota_observations observation
				where observation.id = account_quota_snapshots.observation_id
					and observation.lifecycle_applied = 1
			))
		order by observed_at_ms desc, id desc
		limit ?3
	)
), ranked as (
	select snapshot.*, bounded_ids.source_rank,
		case when snapshot.observation_id is null then 'active' else coalesce((
			select availability from account_quota_windows window
			where window.id = snapshot.logical_window_id
		), 'inactive') end as window_availability
	from bounded_ids
	join account_quota_snapshots snapshot on snapshot.id = bounded_ids.id
)`

// Preserve the existing reader until deferred index maintenance is complete.
// Running the recursive seeks without their index would rescan history.
const legacyCandidateCTE = `with ranked as (
	select
		id, coalesce(observation_id, 0) as observation_id,
		coalesce(logical_window_id, 0) as logical_window_id,
		coalesce(activation_id, 0) as activation_id,
		coalesce(cycle_id, 0) as cycle_id,
		account_key, provider, provider_window_id, window_kind, window_mode,
		model_scope_kind, coalesce(model_scope_key, '') as model_scope_key,
		coalesce(model_ids_json, '') as model_ids_json,
		coalesce(scope_fingerprint, '') as scope_fingerprint,
		coalesce(content_hash, '') as content_hash,
		source, coalesce(source_observation_id, '') as source_observation_id, observed_at_ms,
		boundary_accuracy, cycle_start_ms, cycle_end_ms, duration_seconds,
		used_percent, remaining_percent, used_value, limit_value,
		coalesce(quota_unit, '') as quota_unit, reset_credits_available,
		coalesce(reset_credits_json, '') as reset_credits_json,
		coalesce(plan_type, '') as plan_type, created_at_ms,
		case when observation_id is null then 'active' else coalesce((
			select availability from account_quota_windows window
			where window.id = account_quota_snapshots.logical_window_id
		), 'inactive') end as window_availability,
		row_number() over (
			partition by coalesce(
				cast(logical_window_id as text),
				'legacy:' || provider_window_id || char(0) || model_scope_kind ||
				char(0) || coalesce(model_scope_key, '') || char(0) ||
				coalesce(model_ids_json, '')
			), source
			order by observed_at_ms desc, id desc
		) as source_rank
		from account_quota_snapshots
		where account_key = ?1 and provider = ?2
			and not (instr(coalesce(account_key, ''), ':codex-account:') > 0)
			and (observation_id is null or (
				logical_window_id is not null and exists (
					select 1 from account_quota_observations observation
					where observation.id = account_quota_snapshots.observation_id
						and observation.lifecycle_applied = 1
				)
			))
	)`

const candidateProjection = `
	select
		id, coalesce(observation_id, 0), coalesce(logical_window_id, 0),
		coalesce(activation_id, 0), coalesce(cycle_id, 0),
		account_key, provider, provider_window_id, window_kind, window_mode,
		model_scope_kind, coalesce(model_scope_key, ''), coalesce(model_ids_json, ''),
		coalesce(scope_fingerprint, ''), coalesce(content_hash, ''),
		source, coalesce(source_observation_id, ''), observed_at_ms,
		boundary_accuracy, cycle_start_ms, cycle_end_ms, duration_seconds,
		used_percent, remaining_percent, used_value, limit_value,
		coalesce(quota_unit, ''), reset_credits_available,
		coalesce(reset_credits_json, ''), coalesce(plan_type, ''), created_at_ms
	from ranked
	where source_rank <= ?3
	order by case window_availability when 'active' then 0 when 'pending_absent' then 1 else 2 end,
		source_rank, observed_at_ms desc, id desc
	limit ?4`

func (r *repository) candidateQuery(ctx context.Context) (string, error) {
	var indexed bool
	if err := r.db.QueryRowContext(ctx, `select exists(
		select 1 from sqlite_master where type = 'index' and name = ? and tbl_name = 'account_quota_snapshots'
	)`, CandidateIndexName).Scan(&indexed); err != nil {
		return "", err
	}
	if !indexed {
		return legacyCandidateCTE + candidateProjection, nil
	}
	return boundedCandidateCTE + candidateProjection, nil
}
