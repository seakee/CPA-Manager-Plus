package sqlite

import (
	"database/sql"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const monitoringOutputTPSBackfillKey = "usage_monitoring_output_tps_backfill_v2"

// 只补齐仍有逐请求证据的 TPS，不重建或清空已归档历史的其他统计。
func ensureMonitoringOutputTPS(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	added := false
	for _, column := range []struct{ name, definition string }{
		{"output_tps_sum", "real not null default 0"},
		{"output_tps_samples", "integer not null default 0"},
	} {
		var exists int
		if err := tx.QueryRow(`select count(*) from pragma_table_info('usage_monitoring_account_daily_rollups_v1') where name = ?`, column.name).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			if _, err := tx.Exec(`alter table usage_monitoring_account_daily_rollups_v1 add column ` + column.name + ` ` + column.definition); err != nil {
				return err
			}
			added = true
		}
	}
	var done int
	if err := tx.QueryRow(`select count(*) from settings where key = ?`, monitoringOutputTPSBackfillKey).Scan(&done); err != nil {
		return err
	}
	if done != 0 && !added {
		return tx.Commit()
	}

	// 原始记录优先，投影仅补充已删除的原始记录；两个水位分别限制统计和投影的覆盖范围。
	columns := []string{"timestamp_ms", "account_snapshot", "auth_label_snapshot", "provider", "auth_provider_snapshot", "auth_account_id_snapshot", "auth_index", "source", "source_hash", "auth_file_snapshot", "api_key_hash", "executor_type", "service_tier", "auth_project_id_snapshot", "failed", "output_tokens", "latency_ms"}
	rawColumns, projectedColumns := make([]string, 0, len(columns)), make([]string, 0, len(columns))
	for _, column := range columns {
		rawColumns = append(rawColumns, "e."+column)
		projectedColumns = append(projectedColumns, "p."+column)
	}
	requested := usageidentity.SQLEffectiveRequestedModelExpression("e.model", "e.requested_model")
	analytics := usageidentity.SQLRequestAnalyticsModelExpression("e.model", "e.requested_model")
	query := `create temp table monitoring_output_tps_backfill as
 with state as (select structure_revision, coverage_event_id from usage_monitoring_rollup_state where rollup_name = 'stats_v1'),
 events as (
 select ` + strings.Join(rawColumns, ",") + `, ` + requested + ` as requested_model_value, ` + analytics + ` as analytics_model_value,
 coalesce(nullif(e.resolved_model,''),` + analytics + `) as billing_model_value,
 coalesce(e.normalized_total_input_tokens,e.input_tokens,0) as normalized_input_tokens_value
 from usage_events e where e.id <= (select coverage_event_id from state)
 union all
 select ` + strings.Join(projectedColumns, ",") + `, p.requested_model, p.analytics_model,
 coalesce(nullif(p.resolved_model,''),p.analytics_model),p.normalized_total_input_tokens
 from usage_monitoring_event_projection_v1 p
 where p.event_id <= (select coverage_event_id from state)
 and p.event_id <= (select coverage_event_id from usage_monitoring_rollup_state where rollup_name = 'projection_v1')
 and not exists(select 1 from usage_events e where e.id = p.event_id)
 ), priced as (
 select events.*, case when billing_price.model is not null then billing_model_value
 when analytics_price.model is not null then analytics_model_value
 when display_price.model is not null then requested_model_value else billing_model_value end as pricing_model_value
 from events
 left join model_prices billing_price on billing_price.model = events.billing_model_value
 left join model_prices analytics_price on analytics_price.model = events.analytics_model_value
 left join model_prices display_price on display_price.model = events.requested_model_value
 ), banded as (
 select priced.*, coalesce((select max(threshold_tokens) from model_price_context_tiers tier
 where tier.model = priced.pricing_model_value and priced.normalized_input_tokens_value > tier.threshold_tokens),-1) as context_threshold_tokens_value
 from priced
 ) select (select structure_revision from state) as structure_revision,
 timestamp_ms - timestamp_ms % 86400000 as bucket_ms,
 coalesce(account_snapshot,'') as account_snapshot, coalesce(auth_label_snapshot,'') as auth_label_snapshot,
 coalesce(provider,'') as provider, coalesce(auth_provider_snapshot,'') as auth_provider_snapshot,
 coalesce(auth_account_id_snapshot,'') as auth_account_id_snapshot, coalesce(auth_index,'') as auth_index,
 coalesce(source,'') as source, coalesce(source_hash,'') as source_hash,
 coalesce(auth_file_snapshot,'') as auth_file_snapshot, coalesce(api_key_hash,'') as api_key_hash,
 coalesce(executor_type,'') as executor_type, analytics_model_value as model,
 billing_model_value as billing_model, pricing_model_value as pricing_model,
 coalesce(service_tier,'') as service_tier, context_threshold_tokens_value as context_threshold_tokens,
 sum(output_tokens * 1000.0 / latency_ms) as output_tps_sum, count(*) as output_tps_samples
 from banded where failed = 0 and output_tokens > 0 and latency_ms > 0
 and not (lower(replace(trim(coalesce(nullif(trim(auth_provider_snapshot),''),provider,'')), '_','-')) = 'codex'
 and substr(trim(coalesce(auth_project_id_snapshot,'')),1,length('codex-account-id:v1:')) = 'codex-account-id:v1:')
 group by 1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18`
	if _, err := tx.Exec(query); err != nil {
		return err
	}
	// 日表旧写入按这些维度分组（不含 failed），同组跨批次可能有两种 failed 行。
	// 优先把成功样本放在成功行；没有成功行时保留旧分组，避免改变既有 calls 等字段。
	keys := []string{"structure_revision", "bucket_ms", "account_snapshot", "auth_label_snapshot", "provider", "auth_provider_snapshot", "auth_account_id_snapshot", "auth_index", "source", "source_hash", "auth_file_snapshot", "api_key_hash", "executor_type", "model", "billing_model", "pricing_model", "service_tier", "context_threshold_tokens"}
	matches := make([]string, 0, len(keys))
	for _, key := range keys {
		matches = append(matches, "d."+key+" = b."+key)
	}
	// 先物化目标行，避免每个日表行反复扫描整个回填结果。
	// 若现有 TPS 已包含更多（后来删除的）样本，保留它，不用残缺证据覆盖。
	if _, err := tx.Exec(`create temp table monitoring_output_tps_targets as
	 select d.rowid as target_id,
	 case when d.rowid = (select d.rowid from usage_monitoring_account_daily_rollups_v1 d where ` + strings.Join(matches, " and ") + ` order by d.failed limit 1) then b.output_tps_sum else 0 end as output_tps_sum,
	 case when d.rowid = (select d.rowid from usage_monitoring_account_daily_rollups_v1 d where ` + strings.Join(matches, " and ") + ` order by d.failed limit 1) then b.output_tps_samples else 0 end as output_tps_samples
	 from monitoring_output_tps_backfill b join usage_monitoring_account_daily_rollups_v1 d on ` + strings.Join(matches, " and ") + `
	 where b.output_tps_samples >= (select sum(d.output_tps_samples) from usage_monitoring_account_daily_rollups_v1 d where ` + strings.Join(matches, " and ") + `)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`create unique index monitoring_output_tps_target_id on monitoring_output_tps_targets(target_id)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`update usage_monitoring_account_daily_rollups_v1 as target
	 set (output_tps_sum,output_tps_samples) = (select output_tps_sum,output_tps_samples from monitoring_output_tps_targets where target_id=target.rowid)
	 where target.rowid in (select target_id from monitoring_output_tps_targets)`); err != nil {
		return err
	}
	for _, table := range []string{"monitoring_output_tps_targets", "monitoring_output_tps_backfill"} {
		if _, err := tx.Exec(`drop table ` + table); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`insert or replace into settings(key,value,updated_at_ms) values(?,'completed',0)`, monitoringOutputTPSBackfillKey); err != nil {
		return err
	}
	return tx.Commit()
}
