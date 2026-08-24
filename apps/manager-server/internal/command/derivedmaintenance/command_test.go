package derivedmaintenance

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestRunFinalizesPairedAndOrphanMonitoringFTSJobs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open cleanup fixture: %v", err)
	}
	for _, statement := range []string{
		`create table usage_monitoring_event_projection_v1_legacy_g000001 (
			event_id integer primary key, search_text text not null
		)`,
		`create virtual table usage_monitoring_event_search_v1_legacy_g000001 using fts5(
			search_text,
			content = 'usage_monitoring_event_projection_v1_legacy_g000001',
			content_rowid = 'event_id', columnsize = 0, detail = 'none', tokenize = 'trigram'
		)`,
		`insert into usage_monitoring_event_projection_v1_legacy_g000001 values (1, 'first searchable row'), (2, 'second searchable row')`,
		`insert into usage_monitoring_event_search_v1_legacy_g000001(rowid, search_text)
			select event_id, search_text from usage_monitoring_event_projection_v1_legacy_g000001`,
		`insert into usage_derived_cleanup_jobs (
			generation, kind, status, projection_table, fts_table,
			processed_rows, created_at_ms, updated_at_ms
		) values (1, 'monitoring_fts', 'online_cleanup',
			'usage_monitoring_event_projection_v1_legacy_g000001',
			'usage_monitoring_event_search_v1_legacy_g000001', 0, 1, 1)`,
		`create virtual table usage_monitoring_event_search_v1_legacy_g000002 using fts5(search_text)`,
		`insert into usage_derived_cleanup_jobs (
			generation, kind, status, projection_table, fts_table,
			processed_rows, created_at_ms, updated_at_ms
		) values (2, 'monitoring_fts', 'offline_required', null,
			'usage_monitoring_event_search_v1_legacy_g000002', 0, 1, 1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare cleanup fixture: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close cleanup fixture: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"--db-path", dbPath}, &stdout, &stderr); err != nil {
		t.Fatalf("run derived cleanup: %v stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "jobs=2") || !strings.Contains(stdout.String(), "processed_rows=2") {
		t.Fatalf("cleanup output = %q", stdout.String())
	}
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("reopen cleanup database: %v", err)
	}
	defer db.Close()
	var completed, retained int
	if err := db.QueryRow(`select count(*) from usage_derived_cleanup_jobs where status = 'completed'`).Scan(&completed); err != nil {
		t.Fatalf("count completed jobs: %v", err)
	}
	if err := db.QueryRow(`select count(*) from sqlite_master where type = 'table' and name in (
		'usage_monitoring_event_projection_v1_legacy_g000001',
		'usage_monitoring_event_search_v1_legacy_g000001',
		'usage_monitoring_event_search_v1_legacy_g000002'
	)`).Scan(&retained); err != nil {
		t.Fatalf("count retained cleanup tables: %v", err)
	}
	if completed != 2 || retained != 0 {
		t.Fatalf("cleanup state = completed:%d retained:%d", completed, retained)
	}
}

func TestRunUsesConfiguredDatabaseURL(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	databaseOptions := cleanupDatabaseOptions(dbPath)
	st, err := store.OpenWithOptions(databaseOptions)
	if err != nil {
		t.Fatalf("open DELETE cleanup fixture: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close DELETE cleanup fixture: %v", err)
	}
	for key, value := range map[string]string{
		"CPA_MANAGER_CONFIG": "",
		"USAGE_DATA_DIR":     "",
		"USAGE_DB_PATH":      "",
		"USAGE_DB_URL":       databaseOptions.DataSourceName,
	} {
		t.Setenv(key, value)
	}
	resolved, err := resolveDatabaseOptions("")
	if err != nil {
		t.Fatalf("resolve configured database URL: %v", err)
	}
	if resolved.DataSourceName != databaseOptions.DataSourceName ||
		resolved.ExpectedJournalMode != "delete" ||
		resolved.ExpectedSynchronous != 3 ||
		resolved.ExpectedBusyTimeout != 15000 {
		t.Fatalf("resolved database options = %+v", resolved)
	}

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), nil, &stdout, &stderr); err != nil {
		t.Fatalf("run cleanup with configured URL: %v stderr=%s", err, stderr.String())
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("reopen configured cleanup database: %v", err)
	}
	defer db.Close()
	var journalMode string
	if err := db.QueryRow(`pragma journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read cleanup journal mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "delete") {
		t.Fatalf("cleanup journal mode = %q, want delete", journalMode)
	}
}

func TestRunValidatesConfiguredDatabaseURLBeforeApplyingPragmas(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unrelated.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open unrelated sqlite: %v", err)
	}
	if _, err := db.Exec(`create table unrelated(id integer primary key); pragma user_version = 7`); err != nil {
		t.Fatalf("create unrelated fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close unrelated fixture: %v", err)
	}
	databaseOptions := cleanupDatabaseOptions(dbPath)
	dsn, err := url.Parse(databaseOptions.DataSourceName)
	if err != nil {
		t.Fatalf("parse test database URL: %v", err)
	}
	query := dsn.Query()
	query.Add("_pragma", "user_version(123)")
	dsn.RawQuery = query.Encode()
	for key, value := range map[string]string{
		"CPA_MANAGER_CONFIG": "",
		"USAGE_DATA_DIR":     "",
		"USAGE_DB_PATH":      "",
		"USAGE_DB_URL":       dsn.String(),
	} {
		t.Setenv(key, value)
	}
	var stdout, stderr bytes.Buffer
	err = Run(context.Background(), nil, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "does not look like a CPA Manager Plus") {
		t.Fatalf("Run() error = %v", err)
	}
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("reopen unrelated sqlite: %v", err)
	}
	defer db.Close()
	var userVersion int
	if err := db.QueryRow(`pragma user_version`).Scan(&userVersion); err != nil {
		t.Fatalf("read unrelated user_version: %v", err)
	}
	if userVersion != 7 {
		t.Fatalf("unrelated user_version = %d, want 7", userVersion)
	}
}

func TestRunRejectsMissingDatabase(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{"--db-path", filepath.Join(t.TempDir(), "missing.sqlite")}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "SQLite database not found") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunIsIdempotentAndLeavesMaintenanceStatusClean(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	var firstStdout, firstStderr bytes.Buffer
	if err := Run(context.Background(), []string{"--db-path", dbPath}, &firstStdout, &firstStderr); err != nil {
		t.Fatalf("run initial derived cleanup: %v stderr=%s", err, firstStderr.String())
	}
	if !strings.Contains(firstStdout.String(), "Derived cleanup completed:") {
		t.Fatalf("initial cleanup output = %q", firstStdout.String())
	}

	var secondStdout, secondStderr bytes.Buffer
	if err := Run(context.Background(), []string{"--db-path", dbPath}, &secondStdout, &secondStderr); err != nil {
		t.Fatalf("run repeated derived cleanup: %v stderr=%s", err, secondStderr.String())
	}
	if strings.TrimSpace(secondStdout.String()) != "No pending derived cleanup jobs." {
		t.Fatalf("repeated cleanup output = %q", secondStdout.String())
	}

	st, err = store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	status, err := st.DerivedMaintenanceStatus(context.Background())
	if err != nil {
		t.Fatalf("read maintenance status: %v", err)
	}
	if status.Required || status.PerformanceDegraded || status.DeferredIndexes != 0 || status.OfflineJobs != 0 {
		t.Fatalf("maintenance status after repeated cleanup = %+v", status)
	}
}

func cleanupDatabaseOptions(dbPath string) sqliterepo.Options {
	uriPath := filepath.ToSlash(dbPath)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := url.URL{Scheme: "file", Path: uriPath}
	query := url.Values{}
	query.Add("_txlock", "immediate")
	query.Add("_pragma", "journal_mode(DELETE)")
	query.Add("_pragma", "synchronous(EXTRA)")
	query.Add("_pragma", "busy_timeout(15000)")
	query.Add("_pragma", "foreign_keys(1)")
	dsn.RawQuery = query.Encode()
	return sqliterepo.Options{
		Path:                dbPath,
		DataSourceName:      dsn.String(),
		ExpectedJournalMode: "delete",
		ExpectedSynchronous: 3,
		ExpectedBusyTimeout: 15000,
	}
}

func TestRunRejectsActiveManagerBeforeMutatingCleanupState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open cleanup fixture: %v", err)
	}
	for _, statement := range []string{
		`create virtual table usage_monitoring_event_search_v1_legacy_g000099 using fts5(search_text)`,
		`insert into usage_derived_cleanup_jobs (
			generation, kind, status, projection_table, fts_table,
			processed_rows, created_at_ms, updated_at_ms
		) values (99, 'monitoring_fts', 'offline_required', null,
			'usage_monitoring_event_search_v1_legacy_g000099', 0, 1, 1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare locked cleanup fixture: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close cleanup fixture: %v", err)
	}

	databaseLock, err := processlock.Acquire(dbPath)
	if err != nil {
		t.Fatalf("hold manager process lock: %v", err)
	}
	defer databaseLock.Close()
	var stdout, stderr bytes.Buffer
	err = Run(context.Background(), []string{"--db-path", dbPath}, &stdout, &stderr)
	if !errors.Is(err, processlock.ErrLocked) || !strings.Contains(err.Error(), "stop Manager Server") {
		t.Fatalf("locked cleanup error = %v", err)
	}

	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("reopen locked cleanup fixture: %v", err)
	}
	defer db.Close()
	var status string
	if err := db.QueryRow(`select status from usage_derived_cleanup_jobs where generation = 99`).Scan(&status); err != nil {
		t.Fatalf("read locked cleanup state: %v", err)
	}
	if status != "offline_required" {
		t.Fatalf("locked cleanup status = %q, want offline_required", status)
	}
	var tableCount int
	if err := db.QueryRow(`select count(*) from sqlite_master where type = 'table'
		and name = 'usage_monitoring_event_search_v1_legacy_g000099'`).Scan(&tableCount); err != nil {
		t.Fatalf("inspect locked cleanup table: %v", err)
	}
	if tableCount != 1 {
		t.Fatalf("locked cleanup table count = %d, want 1", tableCount)
	}
}
