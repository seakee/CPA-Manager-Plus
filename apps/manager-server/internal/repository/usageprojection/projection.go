package usageprojection

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

var ErrRetainedCoverageIncomplete = errors.New("retained usage projection coverage is incomplete")

const (
	EventTable       = "usage_monitoring_event_projection_v1"
	HeaderTable      = "usage_monitoring_header_latest_v1"
	SearchIndexTable = "usage_monitoring_event_search_v1"
)

// RetainedEdgeSource joins deleted archive refs to projection rows by event ID.
// The time predicate stays inside each arm so a partial-hour read never scans
// the full projection table. Live raw rows supply the rest of the edge.
func RetainedEdgeSource(fromMS, toMS int64) string {
	columns := []string{
		"timestamp_ms", "model", "requested_model", "resolved_model", "service_tier", "failed",
		"input_tokens", "output_tokens", "reasoning_tokens", "cached_tokens", "cache_tokens",
		"cache_read_tokens", "cache_creation_tokens", "normalized_total_input_tokens", "total_tokens", "latency_ms",
		"provider", "auth_index", "source", "source_hash", "account_snapshot", "auth_label_snapshot",
		"auth_file_snapshot", "auth_provider_snapshot", "auth_project_id_snapshot", "auth_account_id_snapshot",
	}
	return fmt.Sprintf(`(select e.id, e.%s from usage_events e
		where e.timestamp_ms >= %d and e.timestamp_ms < %d
		union all
		select p.event_id as id, p.%s from usage_archive_event_refs archived
		join %s p on p.event_id = archived.raw_event_id
		where archived.raw_deleted_at_ms is not null
			and archived.timestamp_ms >= %d and archived.timestamp_ms < %d)`,
		strings.Join(columns, ", e."), fromMS, toMS,
		strings.Join(columns, ", p."), EventTable, fromMS, toMS)
}

func VerifyRetainedEdgeTx(ctx context.Context, tx *sql.Tx, fromMS, toMS int64) error {
	var version int
	var revision, status string
	var coverageID int64
	if err := tx.QueryRowContext(ctx, `select schema_version, structure_revision, status, coverage_event_id
		from usage_monitoring_rollup_state where rollup_name = 'projection_v1'`).Scan(
		&version, &revision, &status, &coverageID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: missing projection state", ErrRetainedCoverageIncomplete)
		}
		return err
	}
	if version != 1 || revision != usageidentity.MonitoringProjectionStructureRevision() || status == "clearing" {
		return fmt.Errorf("%w: retained usage projection is incompatible or rebuilding", ErrRetainedCoverageIncomplete)
	}
	var incomplete bool
	if err := tx.QueryRowContext(ctx, `select exists (
		select 1 from usage_archive_event_refs archived
		left join usage_monitoring_event_projection_v1 p on p.event_id = archived.raw_event_id
		where archived.raw_deleted_at_ms is not null
			and archived.timestamp_ms >= ? and archived.timestamp_ms < ?
			and (archived.raw_event_id > ? or p.event_id is null)
	)`, fromMS, toMS, coverageID).Scan(&incomplete); err != nil {
		return err
	}
	if incomplete {
		return fmt.Errorf("%w: retained usage projection is missing a deleted edge event", ErrRetainedCoverageIncomplete)
	}
	return nil
}

var SearchColumns = []string{
	"request_id",
	"event_hash",
	"model",
	"requested_model",
	"analytics_model",
	"resolved_model",
	"endpoint",
	"method",
	"path",
	"client_ip",
	"x_forwarded_for",
	"user_agent",
	"source",
	"source_hash",
	"api_key_hash",
	"auth_index",
	"account_snapshot",
	"auth_label_snapshot",
	"auth_file_snapshot",
	"auth_provider_snapshot",
	"auth_project_id_snapshot",
	"reasoning_effort",
	"service_tier",
	"executor_type",
	"fail_summary",
	"header_quota_plan_type",
	"header_error_kind",
	"header_error_code",
	"header_trace_id",
}

// SearchTextExpression builds one lowercase search document per event. The
// control-character separator prevents ordinary substring searches from
// matching text created only by concatenating two adjacent fields.
func SearchTextExpression(prefix string) string {
	parts := make([]string, 0, len(SearchColumns))
	for _, column := range SearchColumns {
		expression := prefix + column
		if column == "analytics_model" {
			expression = usageidentity.SQLRequestAnalyticsModelExpression(prefix+"model", prefix+"requested_model")
		} else if column == "auth_project_id_snapshot" {
			// Historical Codex account markers remain in immutable usage_events,
			// but they are not project identifiers and must not become searchable
			// project text in the monitoring projection.
			expression = usageidentity.SQLProjectIDSnapshotExpression(prefix)
		}
		parts = append(parts, fmt.Sprintf("coalesce(%s, '')", expression))
	}
	return "lower(" + strings.Join(parts, " || char(31) || ") + ")"
}

// SearchIndexLikePattern returns a LIKE pattern that SQLite's FTS5 trigram
// index can accelerate for ordinary substring searches. Short queries and
// control/wildcard input must keep the legacy LIKE path because trigram FTS
// cannot represent them exactly.
func SearchIndexLikePattern(query string) (string, bool) {
	query = strings.TrimSpace(strings.ToLower(query))
	if utf8.RuneCountInString(query) < 3 || strings.ContainsAny(query, "%_\x1f") {
		return "", false
	}
	for _, char := range query {
		if unicode.IsControl(char) {
			return "", false
		}
	}
	return "%" + query + "%", true
}

func SnapshotKeyExpression(prefix string) string {
	column := func(name string) string { return prefix + name }
	return fmt.Sprintf(`case
	when coalesce(%[1]s, '') <> '' and coalesce(%[2]s, '') <> '' then coalesce(%[1]s, '') || '::' || coalesce(%[2]s, '')
	when coalesce(%[1]s, '') <> '' then 'file::' || coalesce(%[1]s, '')
	when coalesce(%[2]s, '') <> '' then 'auth::' || coalesce(%[2]s, '')
	when coalesce(%[3]s, '') <> '' then 'account::' || lower(coalesce(%[3]s, ''))
	when coalesce(%[4]s, '') <> '' then 'source::' || coalesce(%[4]s, '')
	else 'event::' || %[5]s
end`,
		column("auth_file_snapshot"),
		column("auth_index"),
		column("account_snapshot"),
		column("source_hash"),
		column("event_hash"),
	)
}

func UpsertEventRange(ctx context.Context, tx *sql.Tx, afterID, throughID, nowMS int64) error {
	if throughID <= afterID {
		return nil
	}
	return upsertEvents(ctx, tx, "id > ? and id <= ?", []any{afterID, throughID}, nowMS)
}

func UpsertEventIDs(ctx context.Context, tx *sql.Tx, eventIDs []int64, nowMS int64) error {
	if len(eventIDs) == 0 {
		return nil
	}
	encoded, err := json.Marshal(eventIDs)
	if err != nil {
		return err
	}
	return upsertEvents(ctx, tx, "id in (select value from json_each(?))", []any{string(encoded)}, nowMS)
}

func upsertEvents(ctx context.Context, tx *sql.Tx, whereClause string, whereArgs []any, nowMS int64) error {
	query := fmt.Sprintf(`insert into %s (
		event_id, timestamp_ms, search_text, account_key, provider, executor_type, model,
		requested_model, analytics_model, resolved_model, auth_index, source, source_hash, api_key_hash,
		account_snapshot, auth_label_snapshot, auth_file_snapshot,
		auth_provider_snapshot, auth_account_id_snapshot, auth_project_id_snapshot, reasoning_effort,
		service_tier, failed, latency_ms, input_tokens, output_tokens,
		reasoning_tokens, cached_tokens, cache_tokens, cache_read_tokens,
		cache_creation_tokens, normalized_total_input_tokens, total_tokens,
		header_quota_plan_type, header_error_kind, header_error_code,
		header_trace_id, updated_at_ms
	)
	select
		id,
		timestamp_ms,
		%s,
		%s,
		coalesce(provider, ''),
		coalesce(executor_type, ''),
		coalesce(model, ''),
		coalesce(nullif(requested_model, ''), model, ''),
		%s,
		coalesce(resolved_model, ''),
		coalesce(auth_index, ''),
		coalesce(source, ''),
		coalesce(source_hash, ''),
		coalesce(api_key_hash, ''),
		coalesce(account_snapshot, ''),
		coalesce(auth_label_snapshot, ''),
		coalesce(auth_file_snapshot, ''),
		coalesce(auth_provider_snapshot, ''),
		coalesce(auth_account_id_snapshot, ''),
		coalesce(auth_project_id_snapshot, ''),
		coalesce(reasoning_effort, ''),
		coalesce(service_tier, ''),
		coalesce(failed, 0),
		latency_ms,
		coalesce(input_tokens, 0),
		coalesce(output_tokens, 0),
		coalesce(reasoning_tokens, 0),
		coalesce(cached_tokens, 0),
		coalesce(cache_tokens, 0),
		coalesce(cache_read_tokens, 0),
		coalesce(cache_creation_tokens, 0),
		coalesce(normalized_total_input_tokens, input_tokens, 0),
		coalesce(total_tokens, 0),
		coalesce(header_quota_plan_type, ''),
		coalesce(header_error_kind, ''),
		coalesce(header_error_code, ''),
		coalesce(header_trace_id, ''),
		?
	from usage_events
	where %s
	on conflict(event_id) do update set
		timestamp_ms = excluded.timestamp_ms,
		search_text = excluded.search_text,
		account_key = excluded.account_key,
		provider = excluded.provider,
		executor_type = excluded.executor_type,
		model = excluded.model,
		requested_model = excluded.requested_model,
		analytics_model = excluded.analytics_model,
		resolved_model = excluded.resolved_model,
		auth_index = excluded.auth_index,
		source = excluded.source,
		source_hash = excluded.source_hash,
		api_key_hash = excluded.api_key_hash,
		account_snapshot = excluded.account_snapshot,
		auth_label_snapshot = excluded.auth_label_snapshot,
		auth_file_snapshot = excluded.auth_file_snapshot,
		auth_provider_snapshot = excluded.auth_provider_snapshot,
		auth_account_id_snapshot = excluded.auth_account_id_snapshot,
		auth_project_id_snapshot = excluded.auth_project_id_snapshot,
		reasoning_effort = excluded.reasoning_effort,
		service_tier = excluded.service_tier,
		failed = excluded.failed,
		latency_ms = excluded.latency_ms,
		input_tokens = excluded.input_tokens,
		output_tokens = excluded.output_tokens,
		reasoning_tokens = excluded.reasoning_tokens,
		cached_tokens = excluded.cached_tokens,
		cache_tokens = excluded.cache_tokens,
		cache_read_tokens = excluded.cache_read_tokens,
		cache_creation_tokens = excluded.cache_creation_tokens,
		normalized_total_input_tokens = excluded.normalized_total_input_tokens,
		total_tokens = excluded.total_tokens,
		header_quota_plan_type = excluded.header_quota_plan_type,
		header_error_kind = excluded.header_error_kind,
		header_error_code = excluded.header_error_code,
		header_trace_id = excluded.header_trace_id,
		updated_at_ms = excluded.updated_at_ms`, EventTable, SearchTextExpression(""), usageidentity.SQLAccountKeyExpression(""), usageidentity.SQLRequestAnalyticsModelExpression("model", "requested_model"), whereClause)
	args := make([]any, 0, len(whereArgs)+1)
	args = append(args, nowMS)
	args = append(args, whereArgs...)
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

func UpsertHeaderRange(ctx context.Context, tx *sql.Tx, afterID, throughID, nowMS int64) error {
	if throughID <= afterID {
		return nil
	}
	return upsertHeaders(ctx, tx, "id > ? and id <= ?", []any{afterID, throughID}, nowMS)
}

func UpsertHeaderIDs(ctx context.Context, tx *sql.Tx, eventIDs []int64, nowMS int64) error {
	if len(eventIDs) == 0 {
		return nil
	}
	encoded, err := json.Marshal(eventIDs)
	if err != nil {
		return err
	}
	return upsertHeaders(ctx, tx, "id in (select value from json_each(?))", []any{string(encoded)}, nowMS)
}

func upsertHeaders(ctx context.Context, tx *sql.Tx, whereClause string, whereArgs []any, nowMS int64) error {
	query := fmt.Sprintf(`with candidates as (
		select
			id as event_id,
			event_hash,
			timestamp_ms,
			coalesce(auth_file_snapshot, '') as auth_file_snapshot,
			coalesce(auth_index, '') as auth_index,
			coalesce(account_snapshot, '') as account_snapshot,
			coalesce(auth_label_snapshot, '') as auth_label_snapshot,
			coalesce(nullif(auth_provider_snapshot, ''), provider, '') as auth_provider_snapshot,
			coalesce(auth_account_id_snapshot, '') as auth_account_id_snapshot,
			coalesce(auth_project_id_snapshot, '') as auth_project_id_snapshot,
			coalesce(source, '') as source,
			coalesce(source_hash, '') as source_hash,
			coalesce(response_metadata_json, '') as response_metadata_json,
			header_quota_recover_at_ms,
			header_quota_used_percent,
			coalesce(header_quota_plan_type, '') as header_quota_plan_type,
			coalesce(header_error_kind, '') as header_error_kind,
			coalesce(header_error_code, '') as header_error_code,
			coalesce(header_trace_id, '') as header_trace_id,
			%s as snapshot_key
		from usage_events
		where %s
		and (
			coalesce(response_metadata_json, '') <> ''
			or header_quota_recover_at_ms is not null
			or header_quota_used_percent is not null
			or coalesce(header_quota_plan_type, '') <> ''
			or coalesce(header_error_kind, '') <> ''
			or coalesce(header_error_code, '') <> ''
			or coalesce(header_trace_id, '') <> ''
		)
		and (
			coalesce(auth_file_snapshot, '') <> ''
			or coalesce(auth_index, '') <> ''
			or coalesce(account_snapshot, '') <> ''
			or coalesce(source_hash, '') <> ''
		)
	), ranked as (
		select *, row_number() over (
			partition by snapshot_key order by timestamp_ms desc, event_id desc
		) as rn
		from candidates
	)
	insert into %s (
		snapshot_key, event_id, event_hash, timestamp_ms, auth_file_snapshot,
		auth_index, account_snapshot, auth_label_snapshot,
		auth_provider_snapshot, auth_account_id_snapshot, auth_project_id_snapshot, source, source_hash,
		response_metadata_json, header_quota_recover_at_ms,
		header_quota_used_percent, header_quota_plan_type, header_error_kind,
		header_error_code, header_trace_id, updated_at_ms
	)
	select
		snapshot_key, event_id, event_hash, timestamp_ms, auth_file_snapshot,
		auth_index, account_snapshot, auth_label_snapshot,
		auth_provider_snapshot, auth_account_id_snapshot, auth_project_id_snapshot, source, source_hash,
		response_metadata_json, header_quota_recover_at_ms,
		header_quota_used_percent, header_quota_plan_type, header_error_kind,
		header_error_code, header_trace_id, ?
	from ranked where rn = 1
	on conflict(snapshot_key) do update set
		event_id = excluded.event_id,
		event_hash = excluded.event_hash,
		timestamp_ms = excluded.timestamp_ms,
		auth_file_snapshot = excluded.auth_file_snapshot,
		auth_index = excluded.auth_index,
		account_snapshot = excluded.account_snapshot,
		auth_label_snapshot = excluded.auth_label_snapshot,
		auth_provider_snapshot = excluded.auth_provider_snapshot,
		auth_account_id_snapshot = excluded.auth_account_id_snapshot,
		auth_project_id_snapshot = excluded.auth_project_id_snapshot,
		source = excluded.source,
		source_hash = excluded.source_hash,
		response_metadata_json = excluded.response_metadata_json,
		header_quota_recover_at_ms = excluded.header_quota_recover_at_ms,
		header_quota_used_percent = excluded.header_quota_used_percent,
		header_quota_plan_type = excluded.header_quota_plan_type,
		header_error_kind = excluded.header_error_kind,
		header_error_code = excluded.header_error_code,
		header_trace_id = excluded.header_trace_id,
		updated_at_ms = excluded.updated_at_ms
	where excluded.event_id = %s.event_id
		or excluded.timestamp_ms > %s.timestamp_ms
		or (
			excluded.timestamp_ms = %s.timestamp_ms
			and excluded.event_id > %s.event_id
		)`, SnapshotKeyExpression(""), whereClause, HeaderTable, HeaderTable, HeaderTable, HeaderTable, HeaderTable)
	args := make([]any, 0, len(whereArgs)+1)
	args = append(args, whereArgs...)
	args = append(args, nowMS)
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}
