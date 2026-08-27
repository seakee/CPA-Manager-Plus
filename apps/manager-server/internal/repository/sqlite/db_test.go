package sqlite

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDataSourceNameEncodesWindowsDrivePath(t *testing.T) {
	dsn := dataSourceName("C:/CPA Manager/data/usage ? #.sqlite")
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse data source name: %v", err)
	}
	if parsed.Scheme != "file" {
		t.Fatalf("scheme = %q, want file", parsed.Scheme)
	}
	if parsed.Host != "" {
		t.Fatalf("host = %q, want empty", parsed.Host)
	}
	if want := "/C:/CPA Manager/data/usage ? #.sqlite"; parsed.Path != want {
		t.Fatalf("path = %q, want %q", parsed.Path, want)
	}
	wantPragmas := []string{
		"busy_timeout(5000)",
		"foreign_keys(1)",
		"synchronous(FULL)",
	}
	if pragmas := parsed.Query()["_pragma"]; !slices.Equal(pragmas, wantPragmas) {
		t.Fatalf("pragmas = %q, want %q", pragmas, wantPragmas)
	}
	if txLock := parsed.Query().Get("_txlock"); txLock != "immediate" {
		t.Fatalf("txlock = %q, want immediate", txLock)
	}
}

func TestOpenWithOptionsSupportsRelativePath(t *testing.T) {
	t.Chdir(t.TempDir())
	dbPath := filepath.Join("data", "usage.sqlite")
	db, err := OpenWithOptions(Options{Path: dbPath})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("stat sqlite database: %v", err)
	}
}

func TestOpenWithOptionsAppliesConnectionDefaults(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage #.sqlite")
	db, err := OpenWithOptions(Options{Path: dbPath})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	connections := make([]*sql.Conn, 0, defaultMaxOpenConns)
	for i := 0; i < defaultMaxOpenConns; i++ {
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatalf("open connection %d: %v", i, err)
		}
		connections = append(connections, conn)
		assertConnectionPragmas(t, conn)
	}

	stats := db.Stats()
	if stats.MaxOpenConnections != defaultMaxOpenConns {
		t.Fatalf("MaxOpenConnections = %d, want %d", stats.MaxOpenConnections, defaultMaxOpenConns)
	}
	if stats.OpenConnections != defaultMaxOpenConns || stats.InUse != defaultMaxOpenConns {
		t.Fatalf("open/in-use connections = %d/%d, want %d/%d", stats.OpenConnections, stats.InUse, defaultMaxOpenConns, defaultMaxOpenConns)
	}

	for i, conn := range connections {
		if err := conn.Close(); err != nil {
			t.Fatalf("close connection %d: %v", i, err)
		}
	}
	stats = db.Stats()
	if stats.Idle != defaultMaxIdleConns {
		t.Fatalf("idle connections = %d, want %d", stats.Idle, defaultMaxIdleConns)
	}
	if stats.MaxIdleClosed != int64(defaultMaxOpenConns-defaultMaxIdleConns) {
		t.Fatalf("MaxIdleClosed = %d, want %d", stats.MaxIdleClosed, defaultMaxOpenConns-defaultMaxIdleConns)
	}
}

func TestOpenWithOptionsBeginsWriteTransactionsImmediately(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	if _, err := db.Exec(`create table write_lock_test (id integer primary key)`); err != nil {
		t.Fatalf("create write lock fixture: %v", err)
	}

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin write transaction: %v", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	started := make(chan struct{})
	writeResult := make(chan error, 1)
	go func() {
		close(started)
		_, err := db.Exec(`insert into write_lock_test (id) values (1)`)
		writeResult <- err
	}()
	<-started

	select {
	case err := <-writeResult:
		t.Fatalf("competing write completed before transaction release: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback write transaction: %v", err)
	}
	select {
	case err := <-writeResult:
		if err != nil {
			t.Fatalf("competing write after transaction release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("competing write did not resume after transaction release")
	}
}

func TestOpenWithOptionsAppliesCustomDatabaseURLToEveryConnection(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "custom data", "usage ? #.sqlite")
	options := customDatabaseOptions(dbPath, "DELETE", "EXTRA", 15000)
	db, err := OpenWithOptions(options)
	if err != nil {
		t.Fatalf("open custom SQLite URL: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	connections := make([]*sql.Conn, 0, defaultMaxOpenConns)
	for i := 0; i < defaultMaxOpenConns; i++ {
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatalf("open custom connection %d: %v", i, err)
		}
		connections = append(connections, conn)
		assertCustomConnectionPragmas(t, conn)
	}
	var schemaCount int
	if err := connections[0].QueryRowContext(context.Background(), `select count(*) from sqlite_schema
		where type = 'table' and name = 'usage_events'`).Scan(&schemaCount); err != nil {
		t.Fatalf("read migrated schema: %v", err)
	}
	if schemaCount != 1 {
		t.Fatalf("usage_events table count = %d, want 1", schemaCount)
	}
	stats := db.Stats()
	if stats.MaxOpenConnections != defaultMaxOpenConns || stats.OpenConnections != defaultMaxOpenConns {
		t.Fatalf("custom pool stats = %+v", stats)
	}
	for _, conn := range connections {
		if err := conn.Close(); err != nil {
			t.Fatalf("close custom connection: %v", err)
		}
	}
}

func TestOpenWithOptionsRejectsUnsafeOrMismatchedCustomDatabaseURL(t *testing.T) {
	t.Run("missing transaction lock", func(t *testing.T) {
		options := customDatabaseOptions(filepath.Join(t.TempDir(), "usage.sqlite"), "DELETE", "EXTRA", 15000)
		dsn, err := url.Parse(options.DataSourceName)
		if err != nil {
			t.Fatalf("parse test data source: %v", err)
		}
		query := dsn.Query()
		query.Del("_txlock")
		dsn.RawQuery = query.Encode()
		options.DataSourceName = dsn.String()
		if _, err := OpenWithOptions(options); err == nil || !strings.Contains(err.Error(), "_txlock=immediate") {
			t.Fatalf("OpenWithOptions() error = %v", err)
		}
	})

	t.Run("malformed URL is sanitized", func(t *testing.T) {
		const secret = "sqlite-repository-secret-sentinel"
		options := customDatabaseOptions(filepath.Join(t.TempDir(), "usage.sqlite"), "DELETE", "EXTRA", 15000)
		options.DataSourceName = "file:///tmp/%zz?token=" + secret
		if _, err := OpenWithOptions(options); err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), options.DataSourceName) {
			t.Fatalf("OpenWithOptions() error = %v", err)
		}
	})

	t.Run("missing expected settings", func(t *testing.T) {
		options := customDatabaseOptions(filepath.Join(t.TempDir(), "usage.sqlite"), "DELETE", "EXTRA", 15000)
		options.ExpectedBusyTimeout = 0
		if _, err := OpenWithOptions(options); err == nil || !strings.Contains(err.Error(), "positive expected busy timeout") {
			t.Fatalf("OpenWithOptions() error = %v", err)
		}
	})

	t.Run("effective synchronous mismatch", func(t *testing.T) {
		options := customDatabaseOptions(filepath.Join(t.TempDir(), "usage.sqlite"), "DELETE", "FULL", 15000)
		options.ExpectedSynchronous = 3
		if _, err := OpenWithOptions(options); err == nil || !strings.Contains(err.Error(), "synchronous=2, want 3") {
			t.Fatalf("OpenWithOptions() error = %v", err)
		}
	})

	t.Run("effective required pragma override", func(t *testing.T) {
		options := customDatabaseOptions(filepath.Join(t.TempDir(), "usage.sqlite"), "DELETE", "EXTRA", 15000)
		options.DataSourceName += "&_pragma=foreign_keys%3DOFF"
		if _, err := OpenWithOptions(options); err == nil || !strings.Contains(err.Error(), "foreign_keys=0, want 1") {
			t.Fatalf("OpenWithOptions() error = %v", err)
		}
	})

	t.Run("memory mode", func(t *testing.T) {
		options := customDatabaseOptions(filepath.Join(t.TempDir(), "usage.sqlite"), "DELETE", "EXTRA", 15000)
		options.DataSourceName += "&mode=memory"
		if _, err := OpenWithOptions(options); err == nil || !strings.Contains(err.Error(), "persistent file") {
			t.Fatalf("OpenWithOptions() error = %v", err)
		}
	})
}

func TestCustomDatabaseURLJournalTransitionsPreserveData(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open default WAL database: %v", err)
	}
	if _, err := db.Exec(`create table db_url_transition (
		id integer primary key,
		payload text not null
	)`); err != nil {
		t.Fatalf("create transition fixture: %v", err)
	}
	if _, err := db.Exec(`insert into db_url_transition values (1, 'alpha'), (2, 'beta')`); err != nil {
		t.Fatalf("insert transition fixture: %v", err)
	}
	assertJournalMode(t, db, "wal")
	if err := db.Close(); err != nil {
		t.Fatalf("close WAL database: %v", err)
	}

	deleteOptions := customDatabaseOptions(dbPath, "DELETE", "EXTRA", 15000)
	db, err = OpenWithOptions(deleteOptions)
	if err != nil {
		t.Fatalf("open DELETE database: %v", err)
	}
	assertJournalMode(t, db, "delete")
	assertTransitionData(t, db, "1:alpha|2:beta")
	if _, err := db.Exec(`insert into db_url_transition values (3, 'gamma')`); err != nil {
		t.Fatalf("insert DELETE-mode row: %v", err)
	}
	assertSQLiteIntegrity(t, db)
	if err := db.Close(); err != nil {
		t.Fatalf("close DELETE database: %v", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suffix); !os.IsNotExist(err) {
			t.Fatalf("SQLite sidecar %s still exists or stat failed: %v", suffix, err)
		}
	}

	db, err = Open(dbPath)
	if err != nil {
		t.Fatalf("reopen WAL database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	assertJournalMode(t, db, "wal")
	assertTransitionData(t, db, "1:alpha|2:beta|3:gamma")
	assertSQLiteIntegrity(t, db)
}

func customDatabaseOptions(path string, journalMode string, synchronous string, busyTimeout int) Options {
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := url.URL{Scheme: "file", Path: uriPath}
	query := url.Values{}
	query.Add("_txlock", "immediate")
	query.Add("_pragma", "journal_mode("+journalMode+")")
	query.Add("_pragma", "synchronous("+synchronous+")")
	query.Add("_pragma", "busy_timeout("+strconv.Itoa(busyTimeout)+")")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "mmap_size(0)")
	query.Add("_pragma", "cell_size_check(1)")
	query.Add("_pragma", "temp_store(FILE)")
	dsn.RawQuery = query.Encode()
	expectedSynchronous := 2
	if strings.EqualFold(synchronous, "EXTRA") {
		expectedSynchronous = 3
	}
	return Options{
		Path:                path,
		DataSourceName:      dsn.String(),
		ExpectedJournalMode: strings.ToLower(journalMode),
		ExpectedSynchronous: expectedSynchronous,
		ExpectedBusyTimeout: busyTimeout,
	}
}

func assertCustomConnectionPragmas(t *testing.T, conn *sql.Conn) {
	t.Helper()
	for _, test := range []struct {
		name  string
		query string
		want  int
	}{
		{name: "foreign keys", query: "pragma foreign_keys", want: 1},
		{name: "synchronous", query: "pragma synchronous", want: 3},
		{name: "busy timeout", query: "pragma busy_timeout", want: 15000},
		{name: "mmap size", query: "pragma mmap_size", want: 0},
		{name: "cell size check", query: "pragma cell_size_check", want: 1},
		{name: "temp store", query: "pragma temp_store", want: 1},
	} {
		var got int
		if err := conn.QueryRowContext(context.Background(), test.query).Scan(&got); err != nil {
			t.Fatalf("read custom %s: %v", test.name, err)
		}
		if got != test.want {
			t.Fatalf("custom %s = %d, want %d", test.name, got, test.want)
		}
	}
	var journalMode string
	if err := conn.QueryRowContext(context.Background(), `pragma journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read custom journal mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "delete") {
		t.Fatalf("custom journal mode = %q, want delete", journalMode)
	}
}

func assertJournalMode(t *testing.T, db *sql.DB, want string) {
	t.Helper()
	got, err := JournalMode(context.Background(), db)
	if err != nil {
		t.Fatalf("read journal mode: %v", err)
	}
	if got != want {
		t.Fatalf("journal mode = %q, want %q", got, want)
	}
}

func assertTransitionData(t *testing.T, db *sql.DB, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`select group_concat(id || ':' || payload, '|') from (
		select id, payload from db_url_transition order by id
	)`).Scan(&got); err != nil {
		t.Fatalf("read transition data: %v", err)
	}
	if got != want {
		t.Fatalf("transition data = %q, want %q", got, want)
	}
}

func assertSQLiteIntegrity(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, pragma := range []string{"quick_check", "integrity_check"} {
		var result string
		if err := db.QueryRow("pragma " + pragma).Scan(&result); err != nil {
			t.Fatalf("run %s: %v", pragma, err)
		}
		if result != "ok" {
			t.Fatalf("%s = %q, want ok", pragma, result)
		}
	}
}

func assertConnectionPragmas(t *testing.T, conn *sql.Conn) {
	t.Helper()
	for _, test := range []struct {
		name  string
		query string
		want  int
	}{
		{name: "busy timeout", query: "pragma busy_timeout", want: 5000},
		{name: "foreign keys", query: "pragma foreign_keys", want: 1},
		{name: "synchronous", query: "pragma synchronous", want: 2},
	} {
		var got int
		if err := conn.QueryRowContext(context.Background(), test.query).Scan(&got); err != nil {
			t.Fatalf("query %s: %v", test.name, err)
		}
		if got != test.want {
			t.Fatalf("%s = %d, want %d", test.name, got, test.want)
		}
	}
}
