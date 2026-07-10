package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
)

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
