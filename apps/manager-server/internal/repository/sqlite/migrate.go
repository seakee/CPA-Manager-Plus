package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	dashboardHourlyRollupFormatVersionKey = "usage_dashboard_hourly_format_version"
	dashboardHourlyRollupFormatVersion    = "2"
)

func Migrate(db *sql.DB) error {
	statements := []string{
		`pragma journal_mode = WAL`,
		`pragma synchronous = FULL`,
		`pragma busy_timeout = 5000`,
		`pragma foreign_keys = ON`,
		`create table if not exists usage_events (
			id integer primary key autoincrement,
			request_id text,
			event_hash text not null unique,
			timestamp_ms integer not null,
			timestamp text not null,
			provider text,
			executor_type text,
			model text not null,
			endpoint text,
			method text,
			path text,
			auth_type text,
			auth_index text,
			source text,
			source_hash text,
			api_key_hash text,
			account_snapshot text,
			auth_label_snapshot text,
			auth_file_snapshot text,
			auth_provider_snapshot text,
			auth_project_id_snapshot text,
			auth_snapshot_at_ms integer,
			requested_model text,
			resolved_model text,
			reasoning_effort text,
			service_tier text,
			request_service_tier text,
			response_service_tier text,
			cache_input_mode text,
			input_tokens integer not null default 0,
			output_tokens integer not null default 0,
			reasoning_tokens integer not null default 0,
			cached_tokens integer not null default 0,
			cache_tokens integer not null default 0,
			cache_read_tokens integer not null default 0,
			cache_creation_tokens integer not null default 0,
			normalized_uncached_input_tokens integer,
			normalized_total_input_tokens integer,
			normalized_cache_read_tokens integer,
			normalized_cache_creation_tokens integer,
			total_tokens integer not null default 0,
			latency_ms integer,
			ttft_ms integer,
			failed integer not null default 0,
			fail_status_code integer,
			fail_summary text,
			response_metadata_json text,
			header_quota_recover_at_ms integer,
			header_quota_used_percent real,
			header_quota_plan_type text,
			header_error_kind text,
			header_error_code text,
			header_trace_id text,
			fail_body text,
			raw_json text,
			created_at_ms integer not null
		)`,
		`create index if not exists idx_usage_events_timestamp on usage_events(timestamp_ms)`,
		`create index if not exists idx_usage_events_request_id on usage_events(request_id)`,
		`create index if not exists idx_usage_events_model on usage_events(model)`,
		`create index if not exists idx_usage_events_auth_index on usage_events(auth_index)`,
		`create index if not exists idx_usage_events_endpoint on usage_events(endpoint)`,
		`create table if not exists usage_rollup_checkpoints (
			name text primary key,
			last_event_id integer not null default 0,
			updated_at_ms integer not null,
			last_error text,
			last_run_started_at_ms integer,
			last_run_finished_at_ms integer
		)`,
		`create table if not exists usage_account_model_rollups (
			account_key text not null,
			account_snapshot text,
			auth_label_snapshot text,
			auth_provider_snapshot text,
			auth_index text,
			source text,
			source_hash text,
			model text not null,
			billing_model text not null,
			service_tier text not null,
			calls integer not null default 0,
			success_calls integer not null default 0,
			failure_calls integer not null default 0,
			input_tokens integer not null default 0,
			output_tokens integer not null default 0,
			reasoning_tokens integer not null default 0,
			cached_tokens integer not null default 0,
			cache_read_tokens integer not null default 0,
			cache_creation_tokens integer not null default 0,
			long_input_tokens integer not null default 0,
			long_output_tokens integer not null default 0,
			long_cached_tokens integer not null default 0,
			long_cache_read_tokens integer not null default 0,
			long_cache_creation_tokens integer not null default 0,
			total_tokens integer not null default 0,
			first_seen_ms integer not null,
			last_seen_ms integer not null,
			updated_at_ms integer not null,
			primary key (account_key, billing_model, service_tier)
		)`,
		`create index if not exists idx_usage_account_model_rollups_last_seen on usage_account_model_rollups(last_seen_ms)`,
		`create index if not exists idx_usage_account_model_rollups_auth_index on usage_account_model_rollups(auth_index)`,
		`create table if not exists usage_dashboard_hourly_rollups (
			bucket_ms integer not null,
			model text not null,
			billing_model text not null,
			service_tier text not null,
			calls integer not null default 0,
			success_calls integer not null default 0,
			failure_calls integer not null default 0,
			input_tokens integer not null default 0,
			output_tokens integer not null default 0,
			reasoning_tokens integer not null default 0,
			cached_tokens integer not null default 0,
			cache_read_tokens integer not null default 0,
			cache_creation_tokens integer not null default 0,
			long_input_tokens integer not null default 0,
			long_output_tokens integer not null default 0,
			long_cached_tokens integer not null default 0,
			long_cache_read_tokens integer not null default 0,
			long_cache_creation_tokens integer not null default 0,
			total_tokens integer not null default 0,
			latency_sum_ms integer not null default 0,
			latency_samples integer not null default 0,
			zero_token_calls integer not null default 0,
			updated_at_ms integer not null,
			primary key (bucket_ms, model, billing_model, service_tier)
		)`,
		`create table if not exists usage_hourly_aggregate_v1 (
			bucket_ms integer not null,
			model text not null,
			billing_model text not null,
			service_tier text not null,
			failed integer not null,
			calls integer not null default 0,
			input_tokens integer not null default 0,
			output_tokens integer not null default 0,
			reasoning_tokens integer not null default 0,
			cached_tokens integer not null default 0,
			cache_read_tokens integer not null default 0,
			cache_creation_tokens integer not null default 0,
			long_input_tokens integer not null default 0,
			long_output_tokens integer not null default 0,
			long_cached_tokens integer not null default 0,
			long_cache_read_tokens integer not null default 0,
			long_cache_creation_tokens integer not null default 0,
			total_tokens integer not null default 0,
			latency_sum_ms integer not null default 0,
			latency_samples integer not null default 0,
			zero_token_calls integer not null default 0,
			updated_at_ms integer not null,
			primary key (bucket_ms, model, billing_model, service_tier, failed)
		)`,
		`create table if not exists usage_hourly_aggregate_state (
			aggregate_name text primary key,
			schema_version integer not null,
			status text not null,
			backfill_last_event_id integer not null default 0,
			coverage_event_id integer not null default 0,
			target_event_id integer not null default 0,
			processed_events integer not null default 0,
			min_bucket_ms integer,
			max_bucket_ms integer,
			last_run_started_at_ms integer,
			updated_at_ms integer not null default 0,
			finished_at_ms integer,
			last_error text
		)`,
		`insert or ignore into usage_hourly_aggregate_state (
			aggregate_name,
			schema_version,
			status,
			backfill_last_event_id,
			coverage_event_id,
			target_event_id,
			processed_events,
			updated_at_ms
		) select
			'hourly_core',
			1,
			case when exists (select 1 from usage_events limit 1) then 'pending' else 'ready' end,
			0,
			0,
			coalesce((select max(id) from usage_events), 0),
			0,
			0`,
		`create table if not exists usage_event_identity_ledger (
			event_hash text primary key,
			raw_event_id integer,
			timestamp_ms integer not null,
			bucket_ms integer not null,
			aggregate_schema_version integer not null default 0,
			first_seen_at_ms integer not null,
			updated_at_ms integer not null
		)`,
		`create index if not exists idx_usage_event_identity_ledger_raw_event_id on usage_event_identity_ledger(raw_event_id)`,
		`create index if not exists idx_usage_event_identity_ledger_bucket on usage_event_identity_ledger(bucket_ms)`,
		`create table if not exists usage_data_migrations (
			name text primary key,
			status text not null,
			last_event_id integer not null default 0,
			target_event_id integer not null default 0,
			processed_rows integer not null default 0,
			changed_rows integer not null default 0,
			started_at_ms integer,
			updated_at_ms integer not null default 0,
			finished_at_ms integer,
			last_error text
		)`,
		`insert or ignore into usage_data_migrations (
			name, status, last_event_id, target_event_id, processed_rows, updated_at_ms
		) select 'usage_cache_accounting_v1',
			case when exists (select 1 from usage_events limit 1) then 'discovering' else 'completed' end,
			0, 0, 0, 0`,
		`insert or ignore into usage_data_migrations (
			name, status, last_event_id, target_event_id, processed_rows, updated_at_ms
		) select 'usage_cache_accounting_v2',
			case when exists (select 1 from usage_events limit 1) then 'discovering' else 'completed' end,
			0, 0, 0, 0`,
		`create table if not exists usage_cache_accounting_v2_changes (
			event_id integer primary key,
			cache_input_mode text not null,
			normalized_uncached_input_tokens integer not null,
			normalized_total_input_tokens integer not null,
			normalized_cache_read_tokens integer not null,
			normalized_cache_creation_tokens integer not null,
			total_tokens integer not null
		)`,
		`create table if not exists dead_letter_events (
			id integer primary key autoincrement,
			payload text not null,
			error text not null,
			created_at_ms integer not null
		)`,
		`create table if not exists settings (
			key text primary key,
			value text not null,
			updated_at_ms integer not null
		)`,
		`create table if not exists model_prices (
			model text primary key,
			prompt_per_1m real not null,
			completion_per_1m real not null,
			cache_per_1m real not null,
			cache_read_per_1m real not null default 0,
			cache_creation_per_1m real not null default 0,
			prompt_configured integer not null default 0,
			completion_configured integer not null default 0,
			cache_read_configured integer not null default 0,
			cache_creation_configured integer not null default 0,
			source text,
			source_model_id text,
			raw_json text,
			updated_at_ms integer not null,
			synced_at_ms integer
		)`,
		`create table if not exists api_key_aliases (
			api_key_hash text primary key,
			alias text not null,
			updated_at_ms integer not null
		)`,
		`create table if not exists account_action_candidates (
			id integer primary key autoincrement,
			action_type text not null,
			status text not null,
			provider text,
			auth_file_name text not null,
			auth_index text,
			account_snapshot text,
			account_id_snapshot text,
			auth_label text,
			reason_code text,
			reason text,
			auto_disable_eligible integer not null default 0,
			auto_disabled_at_ms integer,
			evidence_json text,
			last_error text,
			first_seen_at_ms integer not null,
			last_seen_at_ms integer not null,
			hit_count integer not null default 1,
			created_at_ms integer not null,
			updated_at_ms integer not null
		)`,
		`drop index if exists idx_account_action_candidates_pending_file_action`,
		`create index if not exists idx_account_action_candidates_status_seen
			on account_action_candidates(status, last_seen_at_ms)`,
		`create table if not exists codex_inspection_runs (
			id integer primary key autoincrement,
			trigger_type text not null,
			trigger_key text,
			status text not null,
			started_at_ms integer not null,
			finished_at_ms integer,
			total_files integer not null default 0,
			probe_set_count integer not null default 0,
			sampled_count integer not null default 0,
			disabled_count integer not null default 0,
			enabled_count integer not null default 0,
			delete_count integer not null default 0,
			disable_count integer not null default 0,
			enable_count integer not null default 0,
			reauth_count integer not null default 0,
			keep_count integer not null default 0,
			error text,
			settings_json text not null,
			created_at_ms integer not null,
			updated_at_ms integer not null
		)`,
		`create index if not exists idx_codex_inspection_runs_started_at on codex_inspection_runs(started_at_ms)`,
		`create index if not exists idx_codex_inspection_runs_status on codex_inspection_runs(status)`,
		`create index if not exists idx_codex_inspection_runs_trigger on codex_inspection_runs(trigger_type, trigger_key)`,
		`create table if not exists codex_inspection_results (
			id integer primary key autoincrement,
			run_id integer not null,
			account_key text not null,
			file_name text not null,
			display_account text not null,
			auth_index text,
			account_id text,
			provider text,
			disabled integer not null default 0,
			status text,
			state text,
			action text not null,
			action_reason text,
			action_status text,
			executed_action text,
			action_error text,
			status_code integer,
			used_percent real,
			is_quota integer not null default 0,
			auto_recover_eligible integer not null default 0,
			error text,
			plan_type text,
			quota_windows_json text,
			error_kind text,
			error_detail text,
			created_at_ms integer not null,
			foreign key(run_id) references codex_inspection_runs(id) on delete cascade,
			unique(run_id, account_key)
		)`,
		`create index if not exists idx_codex_inspection_results_run on codex_inspection_results(run_id)`,
		`create table if not exists codex_inspection_logs (
			id integer primary key autoincrement,
			run_id integer not null,
			level text not null,
			message text not null,
			detail_json text,
			created_at_ms integer not null,
			foreign key(run_id) references codex_inspection_runs(id) on delete cascade
		)`,
		`create index if not exists idx_codex_inspection_logs_run on codex_inspection_logs(run_id, created_at_ms)`,
		`create table if not exists codex_inspection_disable_ownership (
			file_name text primary key,
			provider text not null default 'codex',
			auth_index text,
			account_id text,
			disabled_at_ms integer not null,
			updated_at_ms integer not null
		)`,
		`create table if not exists quota_cooldowns (
			id integer primary key autoincrement,
			auth_file_name text not null,
			auth_index text,
			account_snapshot text,
			provider text,
			reason_code text,
			window_kind text,
			evidence_json text,
			recover_at_ms integer not null,
			owner text not null,
			event_hash text,
			pre_disabled_state integer not null default 0,
			status text not null,
			disabled_at_ms integer not null,
			recovered_at_ms integer,
			last_error text,
			created_at_ms integer not null,
			updated_at_ms integer not null
		)`,
		`create index if not exists idx_quota_cooldowns_due on quota_cooldowns(status, recover_at_ms)`,
		`create unique index if not exists idx_quota_cooldowns_active_owner on quota_cooldowns(auth_file_name, owner) where status = 'active'`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}
	if err := ensureUsageDataMigrationColumns(db); err != nil {
		return err
	}
	if err := ensureUsageEventSnapshotColumns(db); err != nil {
		return err
	}
	if err := ensureCodexInspectionRunColumns(db); err != nil {
		return err
	}
	if err := ensureCodexInspectionResultColumns(db); err != nil {
		return err
	}
	if err := ensureCodexInspectionOwnershipColumns(db); err != nil {
		return err
	}
	if err := ensureAccountActionCandidateColumns(db); err != nil {
		return err
	}
	if err := ensureQuotaCooldownColumns(db); err != nil {
		return err
	}
	if err := ensureUsageRollupLongContextColumns(db); err != nil {
		return err
	}
	if err := ensureDashboardHourlyRollupFormatVersion(db); err != nil {
		return err
	}
	return ensureModelPriceColumns(db)
}

func ensureDashboardHourlyRollupFormatVersion(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var version string
	err = tx.QueryRow(`select value from settings where key = ?`, dashboardHourlyRollupFormatVersionKey).Scan(&version)
	switch {
	case err == nil && version == dashboardHourlyRollupFormatVersion:
		return tx.Commit()
	case err == nil:
		return fmt.Errorf("unsupported dashboard hourly rollup format version %q", version)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}

	for _, statement := range []string{
		`delete from usage_dashboard_hourly_rollups`,
		`delete from usage_rollup_checkpoints where name = 'dashboard_hourly'`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`insert into settings (key, value, updated_at_ms) values (?, ?, ?)`,
		dashboardHourlyRollupFormatVersionKey,
		dashboardHourlyRollupFormatVersion,
		time.Now().UnixMilli(),
	); err != nil {
		return err
	}
	return tx.Commit()
}

func ensureUsageDataMigrationColumns(db *sql.DB) error {
	rows, err := db.Query(`pragma table_info(usage_data_migrations)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	existing := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if _, ok := existing["changed_rows"]; ok {
		return nil
	}
	_, err = db.Exec(`alter table usage_data_migrations add column changed_rows integer not null default 0`)
	return err
}

func ensureCodexInspectionOwnershipColumns(db *sql.DB) error {
	rows, err := db.Query(`pragma table_info(codex_inspection_disable_ownership)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	existing := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if _, ok := existing["provider"]; ok {
		return nil
	}
	if _, err := db.Exec(`alter table codex_inspection_disable_ownership add column provider text not null default 'codex'`); err != nil {
		return err
	}
	return nil
}

func ensureQuotaCooldownColumns(db *sql.DB) error {
	rows, err := db.Query(`pragma table_info(quota_cooldowns)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	existing := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, column := range []struct {
		name       string
		definition string
	}{
		{name: "reason_code", definition: "text"},
		{name: "window_kind", definition: "text"},
		{name: "evidence_json", definition: "text"},
	} {
		if _, ok := existing[column.name]; ok {
			continue
		}
		if _, err := db.Exec(`alter table quota_cooldowns add column ` + column.name + ` ` + column.definition); err != nil {
			return err
		}
	}
	return nil
}

func ensureUsageRollupLongContextColumns(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	columns := []struct {
		name       string
		definition string
	}{
		{name: "long_input_tokens", definition: "integer not null default 0"},
		{name: "long_output_tokens", definition: "integer not null default 0"},
		{name: "long_cached_tokens", definition: "integer not null default 0"},
		{name: "long_cache_read_tokens", definition: "integer not null default 0"},
		{name: "long_cache_creation_tokens", definition: "integer not null default 0"},
	}
	changed := false
	for _, table := range []string{"usage_account_model_rollups", "usage_dashboard_hourly_rollups"} {
		rows, err := tx.Query(`pragma table_info(` + table + `)`)
		if err != nil {
			return err
		}
		existing := map[string]struct{}{}
		for rows.Next() {
			var cid int
			var name, typ string
			var notNull int
			var defaultValue any
			var pk int
			if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
				_ = rows.Close()
				return err
			}
			existing[name] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, column := range columns {
			if _, ok := existing[column.name]; ok {
				continue
			}
			if _, err := tx.Exec(fmt.Sprintf(`alter table %s add column %s %s`, table, column.name, column.definition)); err != nil {
				return err
			}
			changed = true
		}
	}
	if !changed {
		return nil
	}
	for _, statement := range []string{
		`delete from usage_account_model_rollups`,
		`delete from usage_dashboard_hourly_rollups`,
		`delete from usage_rollup_checkpoints where name in ('account_history', 'dashboard_hourly')`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func ensureAccountActionCandidateColumns(db *sql.DB) error {
	rows, err := db.Query(`pragma table_info(account_action_candidates)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	existing := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	columns := []struct {
		name       string
		definition string
	}{
		{name: "account_id_snapshot", definition: "text"},
		{name: "last_error", definition: "text"},
		{name: "reason_code", definition: "text"},
		{name: "auto_disable_eligible", definition: "integer not null default 0"},
		{name: "auto_disabled_at_ms", definition: "integer"},
	}
	for _, column := range columns {
		if _, ok := existing[column.name]; ok {
			continue
		}
		if _, err := db.Exec(`alter table account_action_candidates add column ` + column.name + ` ` + column.definition); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`drop index if exists idx_account_action_candidates_pending_identity_action`); err != nil {
		return err
	}
	if _, err := db.Exec(`create unique index idx_account_action_candidates_pending_identity_action
		on account_action_candidates(auth_file_name, action_type, coalesce(auth_index, ''), coalesce(account_id_snapshot, ''), coalesce(reason_code, '')) where status = 'pending'`); err != nil {
		return err
	}
	_, err = db.Exec(`drop index if exists idx_account_action_candidates_pending_file_action`)
	return err
}

func ensureCodexInspectionRunColumns(db *sql.DB) error {
	rows, err := db.Query(`pragma table_info(codex_inspection_runs)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	existing := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	columns := []struct {
		name       string
		definition string
	}{
		{name: "reauth_count", definition: "integer not null default 0"},
	}
	for _, column := range columns {
		if _, ok := existing[column.name]; ok {
			continue
		}
		if _, err := db.Exec(fmt.Sprintf(
			`alter table codex_inspection_runs add column %s %s`,
			column.name,
			column.definition,
		)); err != nil {
			return err
		}
	}
	return nil
}

func ensureCodexInspectionResultColumns(db *sql.DB) error {
	rows, err := db.Query(`pragma table_info(codex_inspection_results)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	existing := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	columns := []struct {
		name       string
		definition string
	}{
		{name: "action_status", definition: "text"},
		{name: "executed_action", definition: "text"},
		{name: "action_error", definition: "text"},
		{name: "plan_type", definition: "text"},
		{name: "quota_windows_json", definition: "text"},
		{name: "error_kind", definition: "text"},
		{name: "error_detail", definition: "text"},
		{name: "auto_recover_eligible", definition: "integer not null default 0"},
	}
	for _, column := range columns {
		if _, ok := existing[column.name]; ok {
			continue
		}
		if _, err := db.Exec(fmt.Sprintf(
			`alter table codex_inspection_results add column %s %s`,
			column.name,
			column.definition,
		)); err != nil {
			return err
		}
	}
	return nil
}

func ensureUsageEventSnapshotColumns(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.Query(`pragma table_info(usage_events)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	existing := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	columns := []struct {
		name       string
		definition string
	}{
		{name: "account_snapshot", definition: "text"},
		{name: "auth_label_snapshot", definition: "text"},
		{name: "auth_file_snapshot", definition: "text"},
		{name: "auth_provider_snapshot", definition: "text"},
		{name: "auth_project_id_snapshot", definition: "text"},
		{name: "auth_snapshot_at_ms", definition: "integer"},
		{name: "executor_type", definition: "text"},
		{name: "requested_model", definition: "text"},
		{name: "resolved_model", definition: "text"},
		{name: "reasoning_effort", definition: "text"},
		{name: "service_tier", definition: "text"},
		{name: "request_service_tier", definition: "text"},
		{name: "response_service_tier", definition: "text"},
		{name: "cache_input_mode", definition: "text"},
		{name: "cache_read_tokens", definition: "integer not null default 0"},
		{name: "cache_creation_tokens", definition: "integer not null default 0"},
		{name: "normalized_uncached_input_tokens", definition: "integer"},
		{name: "normalized_total_input_tokens", definition: "integer"},
		{name: "normalized_cache_read_tokens", definition: "integer"},
		{name: "normalized_cache_creation_tokens", definition: "integer"},
		{name: "ttft_ms", definition: "integer"},
		{name: "fail_status_code", definition: "integer"},
		{name: "fail_summary", definition: "text"},
		{name: "response_metadata_json", definition: "text"},
		{name: "header_quota_recover_at_ms", definition: "integer"},
		{name: "header_quota_used_percent", definition: "real"},
		{name: "header_quota_plan_type", definition: "text"},
		{name: "header_error_kind", definition: "text"},
		{name: "header_error_code", definition: "text"},
		{name: "header_trace_id", definition: "text"},
		{name: "fail_body", definition: "text"},
	}
	for _, column := range columns {
		if _, ok := existing[column.name]; ok {
			continue
		}
		if _, err := tx.Exec(fmt.Sprintf(
			`alter table usage_events add column %s %s`,
			column.name,
			column.definition,
		)); err != nil {
			return err
		}
	}
	for _, statement := range []string{
		`create index if not exists idx_usage_events_header_quota_recover on usage_events(header_quota_recover_at_ms)`,
		`create index if not exists idx_usage_events_header_error_kind on usage_events(header_error_kind)`,
		`create index if not exists idx_usage_events_header_trace_id on usage_events(header_trace_id)`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func ensureModelPriceColumns(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.Query(`pragma table_info(model_prices)`)
	if err != nil {
		return err
	}

	existing := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	columns := []struct {
		name       string
		definition string
	}{
		{name: "cache_read_per_1m", definition: "real not null default 0"},
		{name: "cache_creation_per_1m", definition: "real not null default 0"},
		{name: "prompt_configured", definition: "integer not null default 0"},
		{name: "completion_configured", definition: "integer not null default 0"},
		{name: "cache_read_configured", definition: "integer not null default 0"},
		{name: "cache_creation_configured", definition: "integer not null default 0"},
	}
	added := map[string]bool{}
	for _, column := range columns {
		if _, ok := existing[column.name]; ok {
			continue
		}
		if _, err := tx.Exec(fmt.Sprintf(
			`alter table model_prices add column %s %s`,
			column.name,
			column.definition,
		)); err != nil {
			return err
		}
		added[column.name] = true
	}
	if added["prompt_configured"] || added["completion_configured"] {
		if _, err := tx.Exec(`update model_prices set prompt_configured = 1, completion_configured = 1`); err != nil {
			return err
		}
	}
	if added["cache_read_configured"] {
		if _, err := tx.Exec(`update model_prices set cache_read_configured = 1 where cache_read_per_1m != 0`); err != nil {
			return err
		}
	}
	if added["cache_creation_configured"] {
		if _, err := tx.Exec(`update model_prices set cache_creation_configured = 1 where cache_creation_per_1m != 0`); err != nil {
			return err
		}
	}
	return tx.Commit()
}
