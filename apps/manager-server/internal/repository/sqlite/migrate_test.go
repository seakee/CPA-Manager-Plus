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
	for _, column := range []string{"reason_code", "window_kind"} {
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
	for _, column := range []string{"reason_code", "window_kind"} {
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
