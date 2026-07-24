package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestUsageDataMigrationInitialStateMatchesExistingUsageData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-data-migration.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open empty sqlite: %v", err)
	}
	var status string
	if err := db.QueryRow(`select status from usage_data_migrations where name = 'usage_cache_accounting_v2'`).Scan(&status); err != nil {
		t.Fatalf("read empty migration state: %v", err)
	}
	if status != "completed" {
		t.Fatalf("empty migration status = %q, want completed", status)
	}
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, input_tokens, created_at_ms
	) values ('legacy', 1, '1', 'gpt-test', 1, 1)`); err != nil {
		t.Fatalf("insert legacy event: %v", err)
	}
	if _, err := db.Exec(`drop table usage_data_migrations`); err != nil {
		t.Fatalf("drop migration table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen legacy sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.QueryRow(`select status from usage_data_migrations where name = 'usage_cache_accounting_v2'`).Scan(&status); err != nil {
		t.Fatalf("read legacy migration state: %v", err)
	}
	if status != "discovering" {
		t.Fatalf("legacy migration status = %q, want discovering", status)
	}
	columns := migrationTableColumns(t, db, "usage_cache_accounting_v2_changes")
	for _, column := range []string{
		"event_id",
		"cache_input_mode",
		"normalized_uncached_input_tokens",
		"normalized_total_input_tokens",
		"normalized_cache_read_tokens",
		"normalized_cache_creation_tokens",
		"total_tokens",
	} {
		if !columns[column] {
			t.Fatalf("staging columns = %#v, missing %s", columns, column)
		}
	}
}

func TestUsageDataMigrationUpgradeAddsChangedRowsAndPreservesV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-data-migration-upgrade.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`drop table usage_data_migrations`,
		`create table usage_data_migrations (name text primary key, status text not null, last_event_id integer not null default 0, target_event_id integer not null default 0, processed_rows integer not null default 0, started_at_ms integer, updated_at_ms integer not null default 0, finished_at_ms integer, last_error text)`,
		`insert into usage_data_migrations (name, status) values ('usage_cache_accounting_v1', 'completed')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("setup legacy sqlite: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("upgrade sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	columns := migrationTableColumns(t, db, "usage_data_migrations")
	if !columns["changed_rows"] {
		t.Fatalf("migration columns = %#v, missing changed_rows", columns)
	}
	var v1Status, v2Status string
	if err := db.QueryRow(`select status from usage_data_migrations where name = 'usage_cache_accounting_v1'`).Scan(&v1Status); err != nil {
		t.Fatalf("read v1 status: %v", err)
	}
	if err := db.QueryRow(`select status from usage_data_migrations where name = 'usage_cache_accounting_v2'`).Scan(&v2Status); err != nil {
		t.Fatalf("read v2 status: %v", err)
	}
	if v1Status != "completed" || v2Status != "completed" {
		t.Fatalf("migration statuses = v1:%q v2:%q", v1Status, v2Status)
	}
}

func TestCodexInspectionAutoRecoverySchema(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "codex-inspection.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	columns := migrationTableColumns(t, db, "codex_inspection_results")
	if !columns["auto_recover_eligible"] {
		t.Fatalf("codex inspection results columns = %#v, want auto_recover_eligible", columns)
	}
	ownershipColumns := migrationTableColumns(t, db, "codex_inspection_disable_ownership")
	for _, column := range []string{"file_name", "auth_index", "account_id", "disabled_at_ms", "updated_at_ms"} {
		if !ownershipColumns[column] {
			t.Fatalf("ownership columns = %#v, missing %s", ownershipColumns, column)
		}
	}
	accountActionColumns := migrationTableColumns(t, db, "account_action_candidates")
	for _, column := range []string{"reason_code", "auto_disable_eligible", "auto_disabled_at_ms"} {
		if !accountActionColumns[column] {
			t.Fatalf("account action columns = %#v, missing %s", accountActionColumns, column)
		}
	}
	cooldownColumns := migrationTableColumns(t, db, "quota_cooldowns")
	for _, column := range []string{"reason_code", "window_kind", "evidence_json"} {
		if !cooldownColumns[column] {
			t.Fatalf("quota cooldown columns = %#v, missing %s", cooldownColumns, column)
		}
	}
}

func TestEnsureAutomationColumnsAddsDecisionMetadata(t *testing.T) {
	db, err := sql.Open("sqlite", dataSourceName(filepath.Join(t.TempDir(), "legacy-automation.sqlite")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`create table account_action_candidates (
		id integer primary key,
		status text,
		auth_file_name text,
		action_type text,
		auth_index text
	)`); err != nil {
		t.Fatalf("create account action table: %v", err)
	}
	if _, err := db.Exec(`create table quota_cooldowns (id integer primary key)`); err != nil {
		t.Fatalf("create quota cooldown table: %v", err)
	}
	if err := ensureAccountActionCandidateColumns(db); err != nil {
		t.Fatalf("migrate account action columns: %v", err)
	}
	if err := ensureQuotaCooldownColumns(db); err != nil {
		t.Fatalf("migrate quota cooldown columns: %v", err)
	}
	for _, column := range []string{"reason_code", "auto_disable_eligible", "auto_disabled_at_ms"} {
		if !migrationTableColumns(t, db, "account_action_candidates")[column] {
			t.Fatalf("missing account action column %s", column)
		}
	}
	for _, column := range []string{"reason_code", "window_kind", "evidence_json"} {
		if !migrationTableColumns(t, db, "quota_cooldowns")[column] {
			t.Fatalf("missing quota cooldown column %s", column)
		}
	}
	if _, err := db.Exec(`insert into account_action_candidates (
		id, status, auth_file_name, action_type, auth_index, reason_code, auto_disable_eligible
	) values
		(1, 'pending', 'xai.json', 'review', '1', 'credential_permission_denied', 1),
		(2, 'pending', 'xai.json', 'review', '1', 'authentication_review', 0)`); err != nil {
		t.Fatalf("insert distinct pending reason codes: %v", err)
	}
}

func TestEnsureCodexInspectionResultColumnsAddsAutoRecoveryEligibility(t *testing.T) {
	db, err := sql.Open("sqlite", dataSourceName(filepath.Join(t.TempDir(), "legacy-codex-inspection.sqlite")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`create table codex_inspection_results (id integer primary key)`); err != nil {
		t.Fatalf("create legacy results table: %v", err)
	}

	if err := ensureCodexInspectionResultColumns(db); err != nil {
		t.Fatalf("migrate codex inspection results: %v", err)
	}
	columns := migrationTableColumns(t, db, "codex_inspection_results")
	if !columns["auto_recover_eligible"] {
		t.Fatalf("legacy results columns = %#v, want auto_recover_eligible", columns)
	}
}

func TestEnsureUsageRollupLongContextColumnsRollsBackAndRetries(t *testing.T) {
	db, err := sql.Open("sqlite", dataSourceName(filepath.Join(t.TempDir(), "rollup-migration.sqlite")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, statement := range []string{
		`create table usage_account_model_rollups (id integer primary key)`,
		`create table usage_dashboard_hourly_rollups (id integer primary key)`,
		`create table usage_rollup_checkpoints (name text primary key)`,
		`insert into usage_account_model_rollups (id) values (1)`,
		`insert into usage_dashboard_hourly_rollups (id) values (1)`,
		`insert into usage_rollup_checkpoints (name) values ('account_history'), ('dashboard_hourly')`,
		`create trigger reject_account_rollup_delete before delete on usage_account_model_rollups
		begin select raise(abort, 'blocked'); end`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("setup migration fixture: %v", err)
		}
	}

	if err := ensureUsageRollupLongContextColumns(db); err == nil {
		t.Fatal("migration error = nil, want trigger failure")
	}
	for _, table := range []string{"usage_account_model_rollups", "usage_dashboard_hourly_rollups"} {
		columns := migrationTableColumns(t, db, table)
		if columns["long_input_tokens"] {
			t.Fatalf("%s columns committed after failed migration: %#v", table, columns)
		}
	}
	assertTableCount(t, db, "usage_account_model_rollups", 1)
	assertTableCount(t, db, "usage_dashboard_hourly_rollups", 1)
	assertTableCount(t, db, "usage_rollup_checkpoints", 2)

	if _, err := db.Exec(`drop trigger reject_account_rollup_delete`); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}
	if err := ensureUsageRollupLongContextColumns(db); err != nil {
		t.Fatalf("retry migration: %v", err)
	}
	for _, table := range []string{"usage_account_model_rollups", "usage_dashboard_hourly_rollups"} {
		columns := migrationTableColumns(t, db, table)
		for _, column := range []string{
			"long_input_tokens",
			"long_output_tokens",
			"long_cached_tokens",
			"long_cache_read_tokens",
			"long_cache_creation_tokens",
		} {
			if !columns[column] {
				t.Fatalf("%s missing column %s after retry: %#v", table, column, columns)
			}
		}
	}
	assertTableCount(t, db, "usage_account_model_rollups", 0)
	assertTableCount(t, db, "usage_dashboard_hourly_rollups", 0)
	assertTableCount(t, db, "usage_rollup_checkpoints", 0)
}

func TestDashboardHourlyRollupFormatUpgradeRebuildsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dashboard-rollup-format.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`insert into usage_events (event_hash, timestamp_ms, timestamp, model, created_at_ms)
		values ('preserved-event', 1, '1', '-', 1)`,
		`insert into usage_dashboard_hourly_rollups (
			bucket_ms, model, billing_model, service_tier, updated_at_ms
		) values (0, '-', '-', '', 1)`,
		`insert into usage_rollup_checkpoints (name, last_event_id, updated_at_ms)
		values ('dashboard_hourly', 1, 1), ('account_history', 1, 1)`,
		`delete from settings where key = 'usage_dashboard_hourly_format_version'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("setup legacy rollup: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("upgrade sqlite: %v", err)
	}
	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, "usage_dashboard_hourly_rollups", 0)
	var dashboardCheckpoints, accountCheckpoints int
	if err := db.QueryRow(`select count(*) from usage_rollup_checkpoints where name = 'dashboard_hourly'`).Scan(&dashboardCheckpoints); err != nil {
		t.Fatalf("read dashboard checkpoint count: %v", err)
	}
	if err := db.QueryRow(`select count(*) from usage_rollup_checkpoints where name = 'account_history'`).Scan(&accountCheckpoints); err != nil {
		t.Fatalf("read account checkpoint count: %v", err)
	}
	if dashboardCheckpoints != 0 || accountCheckpoints != 1 {
		t.Fatalf("checkpoint counts = dashboard:%d account:%d", dashboardCheckpoints, accountCheckpoints)
	}
	var version string
	if err := db.QueryRow(`select value from settings where key = ?`, dashboardHourlyRollupFormatVersionKey).Scan(&version); err != nil {
		t.Fatalf("read rollup format version: %v", err)
	}
	if version != dashboardHourlyRollupFormatVersion {
		t.Fatalf("rollup format version = %q, want %q", version, dashboardHourlyRollupFormatVersion)
	}
	if _, err := db.Exec(`insert into usage_dashboard_hourly_rollups (
		bucket_ms, model, billing_model, service_tier, updated_at_ms
	) values (0, '-', '-', '', 2)`); err != nil {
		_ = db.Close()
		t.Fatalf("insert rebuilt rollup: %v", err)
	}
	if _, err := db.Exec(`insert into usage_rollup_checkpoints (name, last_event_id, updated_at_ms)
		values ('dashboard_hourly', 1, 2)`); err != nil {
		_ = db.Close()
		t.Fatalf("insert rebuilt checkpoint: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close upgraded sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen upgraded sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	assertTableCount(t, db, "usage_dashboard_hourly_rollups", 1)
	if err := db.QueryRow(`select count(*) from usage_rollup_checkpoints where name = 'dashboard_hourly'`).Scan(&dashboardCheckpoints); err != nil {
		t.Fatalf("read preserved dashboard checkpoint: %v", err)
	}
	if dashboardCheckpoints != 1 {
		t.Fatalf("dashboard checkpoint count after idempotent reopen = %d, want 1", dashboardCheckpoints)
	}
}

func TestDashboardHourlyRollupFormatUpgradeRollsBackAndRetries(t *testing.T) {
	db, err := sql.Open("sqlite", dataSourceName(filepath.Join(t.TempDir(), "dashboard-rollup-format-retry.sqlite")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`create table settings (key text primary key, value text not null, updated_at_ms integer not null)`,
		`create table usage_dashboard_hourly_rollups (id integer primary key)`,
		`create table usage_rollup_checkpoints (name text primary key)`,
		`insert into usage_dashboard_hourly_rollups (id) values (1)`,
		`insert into usage_rollup_checkpoints (name) values ('dashboard_hourly'), ('account_history')`,
		`create trigger reject_dashboard_rollup_delete before delete on usage_dashboard_hourly_rollups
		begin select raise(abort, 'blocked'); end`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("setup migration fixture: %v", err)
		}
	}

	if err := ensureDashboardHourlyRollupFormatVersion(db); err == nil {
		t.Fatal("format migration error = nil, want trigger failure")
	}
	assertTableCount(t, db, "usage_dashboard_hourly_rollups", 1)
	assertTableCount(t, db, "usage_rollup_checkpoints", 2)
	var settingCount int
	if err := db.QueryRow(`select count(*) from settings where key = ?`, dashboardHourlyRollupFormatVersionKey).Scan(&settingCount); err != nil {
		t.Fatalf("read format setting count: %v", err)
	}
	if settingCount != 0 {
		t.Fatalf("format setting count after rollback = %d, want 0", settingCount)
	}

	if _, err := db.Exec(`drop trigger reject_dashboard_rollup_delete`); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}
	if err := ensureDashboardHourlyRollupFormatVersion(db); err != nil {
		t.Fatalf("retry format migration: %v", err)
	}
	assertTableCount(t, db, "usage_dashboard_hourly_rollups", 0)
	var dashboardCheckpoints, accountCheckpoints int
	if err := db.QueryRow(`select count(*) from usage_rollup_checkpoints where name = 'dashboard_hourly'`).Scan(&dashboardCheckpoints); err != nil {
		t.Fatalf("read dashboard checkpoint count: %v", err)
	}
	if err := db.QueryRow(`select count(*) from usage_rollup_checkpoints where name = 'account_history'`).Scan(&accountCheckpoints); err != nil {
		t.Fatalf("read account checkpoint count: %v", err)
	}
	if dashboardCheckpoints != 0 || accountCheckpoints != 1 {
		t.Fatalf("checkpoint counts after retry = dashboard:%d account:%d", dashboardCheckpoints, accountCheckpoints)
	}
}

func TestDashboardHourlyRollupFormatUpgradeRejectsUnknownVersionWithoutMutation(t *testing.T) {
	db, err := sql.Open("sqlite", dataSourceName(filepath.Join(t.TempDir(), "dashboard-rollup-format-unknown.sqlite")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`create table settings (key text primary key, value text not null, updated_at_ms integer not null)`,
		`create table usage_dashboard_hourly_rollups (id integer primary key)`,
		`create table usage_rollup_checkpoints (name text primary key)`,
		`insert into settings (key, value, updated_at_ms)
		values ('usage_dashboard_hourly_format_version', 'future', 1)`,
		`insert into usage_dashboard_hourly_rollups (id) values (1)`,
		`insert into usage_rollup_checkpoints (name) values ('dashboard_hourly'), ('account_history')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("setup unknown format fixture: %v", err)
		}
	}

	if err := ensureDashboardHourlyRollupFormatVersion(db); err == nil {
		t.Fatal("format migration error = nil, want unsupported version failure")
	}
	assertTableCount(t, db, "usage_dashboard_hourly_rollups", 1)
	assertTableCount(t, db, "usage_rollup_checkpoints", 2)
	var version string
	if err := db.QueryRow(`select value from settings where key = ?`, dashboardHourlyRollupFormatVersionKey).Scan(&version); err != nil {
		t.Fatalf("read preserved format version: %v", err)
	}
	if version != "future" {
		t.Fatalf("format version after rejection = %q, want future", version)
	}
}

func TestUsageHourlyAggregateMigrationIsAdditiveAndSeedsPendingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-hourly-aggregate.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`drop table usage_event_identity_ledger`,
		`drop table usage_hourly_aggregate_state`,
		`drop table usage_hourly_aggregate_v1`,
		`insert into usage_events (event_hash, timestamp_ms, timestamp, model, created_at_ms)
		values ('aggregate-migration-event', 3600001, '1970-01-01T01:00:00.001Z', 'gpt-test', 1)`,
		`insert into usage_dashboard_hourly_rollups (
			bucket_ms, model, billing_model, service_tier, updated_at_ms
		) values (3600000, 'gpt-test', 'gpt-test', '', 1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("setup legacy aggregate database: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy aggregate database: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen migrated aggregate database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, table := range []string{
		"usage_hourly_aggregate_v1",
		"usage_hourly_aggregate_state",
		"usage_event_identity_ledger",
	} {
		var count int
		if err := db.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("query sqlite_master for %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("expected table %s to exist", table)
		}
	}
	columns := migrationTableColumns(t, db, "usage_hourly_aggregate_v1")
	for _, column := range []string{"bucket_ms", "model", "billing_model", "service_tier", "failed", "latency_sum_ms", "latency_samples"} {
		if !columns[column] {
			t.Fatalf("aggregate columns = %#v, missing %s", columns, column)
		}
	}
	var version int
	var status string
	var checkpoint, coverage, target int64
	if err := db.QueryRow(`select schema_version, status, backfill_last_event_id, coverage_event_id, target_event_id
		from usage_hourly_aggregate_state where aggregate_name = 'hourly_core'`).Scan(
		&version,
		&status,
		&checkpoint,
		&coverage,
		&target,
	); err != nil {
		t.Fatalf("read aggregate state: %v", err)
	}
	if version != 1 || status != "pending" || checkpoint != 0 || coverage != 0 || target != 1 {
		t.Fatalf("aggregate state = version:%d status:%q checkpoint:%d coverage:%d target:%d", version, status, checkpoint, coverage, target)
	}
	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, "usage_dashboard_hourly_rollups", 1)
}

func TestEnsureUsageEventSnapshotColumnsOnlyMigratesSchema(t *testing.T) {
	db, err := sql.Open("sqlite", dataSourceName(filepath.Join(t.TempDir(), "usage-event-migration.sqlite")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, statement := range []string{
		`create table usage_events (
			id integer primary key,
			provider text,
			model text not null,
			input_tokens integer not null default 0,
			cached_tokens integer not null default 0,
			cache_tokens integer not null default 0
		)`,
		`insert into usage_events (id, provider, model, input_tokens, cached_tokens, cache_tokens)
		values (1, 'anthropic', 'claude-sonnet-4', 100, 300, 0)`,
		`create table usage_account_model_rollups (id integer primary key)`,
		`create table usage_dashboard_hourly_rollups (id integer primary key)`,
		`create table usage_rollup_checkpoints (
			name text primary key,
			last_event_id integer not null,
			updated_at_ms integer not null,
			last_error text
		)`,
		`insert into usage_account_model_rollups (id) values (1)`,
		`insert into usage_dashboard_hourly_rollups (id) values (1)`,
		`insert into usage_rollup_checkpoints (name, last_event_id, updated_at_ms, last_error)
		values ('account_history', 1, 1, 'old')`,
		`create trigger reject_usage_event_update before update on usage_events
		begin select raise(abort, 'blocked'); end`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("setup migration fixture: %v", err)
		}
	}

	if err := ensureUsageEventSnapshotColumns(db); err != nil {
		t.Fatalf("migrate usage event schema: %v", err)
	}
	columns := migrationTableColumns(t, db, "usage_events")
	if !columns["cache_input_mode"] || !columns["normalized_total_input_tokens"] {
		t.Fatalf("usage event schema columns = %#v", columns)
	}
	assertTableCount(t, db, "usage_account_model_rollups", 1)
	assertTableCount(t, db, "usage_dashboard_hourly_rollups", 1)
	var normalizedTotal sql.NullInt64
	if err := db.QueryRow(`select normalized_total_input_tokens from usage_events where id = 1`).Scan(&normalizedTotal); err != nil {
		t.Fatalf("read partially migrated usage event: %v", err)
	}
	if normalizedTotal.Valid {
		t.Fatalf("schema migration unexpectedly backfilled normalized total: %d", normalizedTotal.Int64)
	}
}

func TestEnsureModelPriceColumnsPreservesLegacyZeroBasePrices(t *testing.T) {
	db, err := sql.Open("sqlite", dataSourceName(filepath.Join(t.TempDir(), "model-price-migration.sqlite")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, statement := range []string{
		`create table model_prices (
			model text primary key,
			prompt_per_1m real not null,
			completion_per_1m real not null,
			cache_per_1m real not null
		)`,
		`insert into model_prices (model, prompt_per_1m, completion_per_1m, cache_per_1m)
		values ('gpt-5.6-sol', 0, 0, 0)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("setup model price fixture: %v", err)
		}
	}

	if err := ensureModelPriceColumns(db); err != nil {
		t.Fatalf("migrate model prices: %v", err)
	}
	var promptConfigured, completionConfigured, cacheReadConfigured, cacheCreationConfigured int
	if err := db.QueryRow(`select prompt_configured, completion_configured, cache_read_configured, cache_creation_configured
		from model_prices where model = 'gpt-5.6-sol'`).Scan(
		&promptConfigured,
		&completionConfigured,
		&cacheReadConfigured,
		&cacheCreationConfigured,
	); err != nil {
		t.Fatalf("read migrated price flags: %v", err)
	}
	if promptConfigured != 1 || completionConfigured != 1 || cacheReadConfigured != 0 || cacheCreationConfigured != 0 {
		t.Fatalf("configured flags = %d/%d/%d/%d", promptConfigured, completionConfigured, cacheReadConfigured, cacheCreationConfigured)
	}
}

func migrationTableColumns(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`pragma table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("read %s columns: %v", table, err)
	}
	defer rows.Close()

	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan %s columns: %v", table, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s columns: %v", table, err)
	}
	return columns
}

func assertTableCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`select count(*) from ` + table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}
