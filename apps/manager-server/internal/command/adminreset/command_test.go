package adminreset

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestRunGeneratesAdminKey(t *testing.T) {
	dbPath := newCommandTestDB(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if err := Run(context.Background(), []string{"--db-path", dbPath}, &stdout, &stderr); err != nil {
		t.Fatalf("run reset command: %v stderr=%s", err, stderr.String())
	}

	output := stdout.String()
	const marker = "New admin key: "
	index := strings.Index(output, marker)
	if index < 0 {
		t.Fatalf("output does not contain generated key: %s", output)
	}
	adminKey := strings.TrimSpace(strings.Split(output[index+len(marker):], "\n")[0])
	if !strings.HasPrefix(adminKey, "cpamp_") {
		t.Fatalf("generated key = %q", adminKey)
	}
	requireAdminKeyVerifies(t, dbPath, adminKey)
}

func TestRunUsesProvidedAdminKeyWithoutEchoingIt(t *testing.T) {
	dbPath := newCommandTestDB(t)
	const adminKey = "cpamp_from_cli"
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if err := Run(context.Background(), []string{"--db-path", dbPath, "--admin-key", adminKey}, &stdout, &stderr); err != nil {
		t.Fatalf("run reset command: %v stderr=%s", err, stderr.String())
	}

	if strings.Contains(stdout.String(), adminKey) {
		t.Fatalf("stdout leaked provided admin key: %s", stdout.String())
	}
	requireAdminKeyVerifies(t, dbPath, adminKey)
}

func TestRunUsesConfiguredDatabaseURLWithoutChangingJournalMode(t *testing.T) {
	dbPath := newCommandTestDB(t)
	databaseOptions := commandDatabaseOptions(dbPath)
	st, err := store.OpenWithOptions(databaseOptions)
	if err != nil {
		t.Fatalf("switch command fixture to DELETE mode: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close DELETE fixture: %v", err)
	}
	for key, value := range map[string]string{
		"CPA_MANAGER_CONFIG": "",
		"USAGE_DATA_DIR":     "",
		"USAGE_DB_PATH":      "",
		"USAGE_DB_URL":       databaseOptions.DataSourceName,
	} {
		t.Setenv(key, value)
	}
	const adminKey = "cpamp_from_database_url"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := Run(context.Background(), []string{"--admin-key", adminKey}, &stdout, &stderr); err != nil {
		t.Fatalf("run reset command with configured URL: %v stderr=%s", err, stderr.String())
	}
	requireAdminKeyVerifiesWithOptions(t, databaseOptions, adminKey)
	requireJournalMode(t, dbPath, "delete")
}

func TestRunRejectsMissingDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "missing.sqlite")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := Run(context.Background(), []string{"--db-path", dbPath}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "SQLite database not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunRejectsEmptyDBFile(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
		t.Fatalf("write empty db file: %v", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := Run(context.Background(), []string{"--db-path", dbPath}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunRejectsUnrelatedSQLiteDBWithoutMigratingIt(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open unrelated sqlite: %v", err)
	}
	if _, err := db.Exec(`create table unrelated(id integer primary key)`); err != nil {
		t.Fatalf("create unrelated table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close unrelated sqlite: %v", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err = Run(context.Background(), []string{"--db-path", dbPath}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "does not look like a CPA Manager Plus") {
		t.Fatalf("err = %v", err)
	}
	requireNoManagerTables(t, dbPath)
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
	databaseOptions := commandDatabaseOptions(dbPath)
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
	requireNoManagerTables(t, dbPath)
}

func TestRunRejectsConflictingAdminKeyInputs(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := Run(context.Background(), []string{"--admin-key", "one", "--admin-key-file", "two"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunRejectsActiveManagerWithoutChangingAdminKey(t *testing.T) {
	dbPath := newCommandTestDB(t)
	databaseLock, err := processlock.Acquire(dbPath)
	if err != nil {
		t.Fatalf("acquire active manager lock: %v", err)
	}
	t.Cleanup(func() { _ = databaseLock.Close() })

	const replacementKey = "cpamp_replacement"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err = Run(context.Background(), []string{"--db-path", dbPath, "--admin-key", replacementKey}, &stdout, &stderr)
	if !errors.Is(err, processlock.ErrLocked) || !strings.Contains(err.Error(), "stop Manager Server") {
		t.Fatalf("err = %v, want process lock guidance", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no success output", stdout.String())
	}
	requireAdminKeyVerifies(t, dbPath, "cpamp_old")
	requireAdminKeyDoesNotVerify(t, dbPath, replacementKey)
}

func newCommandTestDB(t testing.TB) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	credential, err := security.NewAdminCredential("cpamp_old", "test")
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	if err := st.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatalf("save credential: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	return dbPath
}

func commandDatabaseOptions(dbPath string) sqliterepo.Options {
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

func requireAdminKeyVerifiesWithOptions(t testing.TB, options sqliterepo.Options, adminKey string) {
	t.Helper()
	st, err := store.OpenWithOptions(options)
	if err != nil {
		t.Fatalf("open custom store: %v", err)
	}
	defer st.Close()
	credential, ok, err := st.LoadAdminCredential(context.Background())
	if err != nil || !ok {
		t.Fatalf("load credential ok=%v err=%v", ok, err)
	}
	if !security.VerifyAdminKey(credential, adminKey) {
		t.Fatalf("admin key %q does not verify", adminKey)
	}
}

func requireJournalMode(t testing.TB, dbPath string, want string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite journal check: %v", err)
	}
	defer db.Close()
	var got string
	if err := db.QueryRow(`pragma journal_mode`).Scan(&got); err != nil {
		t.Fatalf("read journal mode: %v", err)
	}
	if !strings.EqualFold(got, want) {
		t.Fatalf("journal mode = %q, want %q", got, want)
	}
}

func requireNoManagerTables(t testing.TB, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow(`select count(*) from sqlite_schema where type = 'table' and name in ('settings', 'usage_events')`).Scan(&count); err != nil {
		t.Fatalf("count manager tables: %v", err)
	}
	if count != 0 {
		t.Fatalf("manager table count = %d, want 0", count)
	}
}

func requireAdminKeyVerifies(t testing.TB, dbPath string, adminKey string) {
	t.Helper()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	credential, ok, err := st.LoadAdminCredential(context.Background())
	if err != nil || !ok {
		t.Fatalf("load credential ok=%v err=%v", ok, err)
	}
	if !security.VerifyAdminKey(credential, adminKey) {
		t.Fatalf("admin key %q does not verify", adminKey)
	}
}

func requireAdminKeyDoesNotVerify(t testing.TB, dbPath string, adminKey string) {
	t.Helper()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	credential, ok, err := st.LoadAdminCredential(context.Background())
	if err != nil || !ok {
		t.Fatalf("load credential ok=%v err=%v", ok, err)
	}
	if security.VerifyAdminKey(credential, adminKey) {
		t.Fatalf("admin key %q unexpectedly verifies", adminKey)
	}
}
