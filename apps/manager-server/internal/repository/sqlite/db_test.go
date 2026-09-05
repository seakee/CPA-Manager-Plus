package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
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

func TestRequireExistingDataKeyRejectsMissingKeyForEncryptedCPAConnection(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	protector, err := security.NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("create protector: %v", err)
	}
	db, err := sql.Open("sqlite", dataSourceName(dbPath))
	if err != nil {
		t.Fatalf("open fixture sqlite: %v", err)
	}
	if err := Migrate(db); err != nil {
		_ = db.Close()
		t.Fatalf("migrate fixture sqlite: %v", err)
	}
	// Keep this fixture at the raw storage boundary: the guard must recognize
	// the encrypted envelope without opening the normal protected Store.
	protected, err := protector.ProtectString("cpa-management-key")
	if err != nil {
		_ = db.Close()
		t.Fatalf("protect fixture key: %v", err)
	}
	if _, err := db.Exec(`insert into settings(key, value, updated_at_ms) values(?, ?, 1)`,
		"manager_config_v1",
		`{"cpaConnection":{"cpaBaseUrl":"http://cpa.local:8317","managementKey":"`+protected+`"}}`,
	); err != nil {
		_ = db.Close()
		t.Fatalf("write encrypted fixture setting: %v", err)
	}
	if _, err := db.Exec(`insert into settings(key, value, updated_at_ms) values(?, ?, 1)`,
		"bootstrap_state_v1",
		`{"connectionStorageMigrationVersion":2}`,
	); err != nil {
		_ = db.Close()
		t.Fatalf("write migrated bootstrap state: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture sqlite: %v", err)
	}

	dataKeyPath := filepath.Join(t.TempDir(), "missing-data.key")
	err = RequireExistingDataKeyForEncryptedCPAConnection(
		context.Background(),
		dbPath,
		"",
		dataKeyPath,
	)
	if err == nil || !strings.Contains(err.Error(), "data key is missing") {
		t.Fatalf("missing data key guard error = %v", err)
	}
	if _, statErr := os.Stat(dataKeyPath); !os.IsNotExist(statErr) {
		t.Fatalf("guard unexpectedly created data key: %v", statErr)
	}
}

func TestRequireExistingDataKeyAllowsLegacyPrefixPlaintext(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sql.Open("sqlite", dataSourceName(dbPath))
	if err != nil {
		t.Fatalf("open fixture sqlite: %v", err)
	}
	if err := Migrate(db); err != nil {
		_ = db.Close()
		t.Fatalf("migrate fixture sqlite: %v", err)
	}
	if _, err := db.Exec(`insert into settings(key, value, updated_at_ms) values(?, ?, 1)`,
		"manager_config_v1",
		`{"cpaConnection":{"cpaBaseUrl":"http://cpa.local:8317","managementKey":"enc:v1:legacy-real-key"}}`,
	); err != nil {
		_ = db.Close()
		t.Fatalf("write legacy fixture setting: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture sqlite: %v", err)
	}

	if err := RequireExistingDataKeyForEncryptedCPAConnection(
		context.Background(),
		dbPath,
		"",
		filepath.Join(t.TempDir(), "new-data.key"),
	); err != nil {
		t.Fatalf("legacy prefix plaintext incorrectly rejected: %v", err)
	}
}

func TestInspectPersistedCPAConnectionStorageRejectsInvalidPostV2ManagementKey(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{
			name:  "setup malformed envelope",
			key:   "setup",
			value: `{"cpaBaseUrl":"http://cpa.local:8317","managementKey":"enc:v1:broken"}`,
		},
		{
			name:  "manager config malformed envelope",
			key:   "manager_config_v1",
			value: `{"cpaConnection":{"cpaBaseUrl":"http://cpa.local:8317","managementKey":"enc:v1:broken"}}`,
		},
		{
			name:  "setup plaintext",
			key:   "setup",
			value: `{"cpaBaseUrl":"http://cpa.local:8317","managementKey":"plain-old-key"}`,
		},
		{
			name:  "manager config plaintext",
			key:   "manager_config_v1",
			value: `{"cpaConnection":{"cpaBaseUrl":"http://cpa.local:8317","managementKey":"plain-old-key"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
			db, err := sql.Open("sqlite", dataSourceName(dbPath))
			if err != nil {
				t.Fatalf("open fixture sqlite: %v", err)
			}
			if err := Migrate(db); err != nil {
				_ = db.Close()
				t.Fatalf("migrate fixture sqlite: %v", err)
			}
			for _, setting := range []struct {
				key   string
				value string
			}{
				{key: "bootstrap_state_v1", value: `{"connectionStorageMigrationVersion":2}`},
				{key: tt.key, value: tt.value},
			} {
				if _, err := db.Exec(`insert into settings(key, value, updated_at_ms) values(?, ?, 1)`, setting.key, setting.value); err != nil {
					_ = db.Close()
					t.Fatalf("write fixture setting %s: %v", setting.key, err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatalf("close fixture sqlite: %v", err)
			}

			_, err = InspectPersistedCPAConnectionStorage(context.Background(), dbPath)
			if err == nil || !strings.Contains(err.Error(), "corrupted persisted CPA connection") {
				t.Fatalf("inspection error = %v, want corrupted persisted CPA connection", err)
			}
		})
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

func TestOpenDiagnosticForGetTempPath(t *testing.T) {
	message := openDiagnostic(sqlite3.SQLITE_IOERR_GETTEMPPATH, "/data/usage.sqlite")
	for _, want := range []string{"temporary directory", "writable"} {
		if !strings.Contains(message, want) {
			t.Fatalf("diagnostic %q does not mention %q", message, want)
		}
	}
	for _, platformSpecific := range []string{"/tmp", "SQLITE_TMPDIR"} {
		if strings.Contains(message, platformSpecific) {
			t.Fatalf("diagnostic %q must not hard-code platform-specific guidance %q", message, platformSpecific)
		}
	}
}

func TestOpenDiagnosticForReadonly(t *testing.T) {
	message := openDiagnostic(sqlite3.SQLITE_READONLY, "/data/usage.sqlite")
	for _, want := range []string{"not writable", "/data/usage.sqlite", "ownership", "permissions"} {
		if !strings.Contains(message, want) {
			t.Fatalf("diagnostic %q does not mention %q", message, want)
		}
	}
}

func TestOpenDiagnosticForUnrecognizedCodes(t *testing.T) {
	for _, code := range []int{
		sqlite3.SQLITE_CANTOPEN,
		sqlite3.SQLITE_BUSY,
		sqlite3.SQLITE_IOERR,
		sqlite3.SQLITE_NOTADB,
		sqlite3.SQLITE_READONLY_DIRECTORY,
		sqlite3.SQLITE_READONLY_DBMOVED,
	} {
		if message := openDiagnostic(code, "/data/usage.sqlite"); message != "" {
			t.Fatalf("diagnostic %q returned for code %d, want no diagnostic", message, code)
		}
	}
}

func TestEnrichOpenErrorPassesUnknownErrorsThrough(t *testing.T) {
	plainErr := errors.New("boom")
	err := enrichOpenError("/data/usage.sqlite", plainErr)
	if err != plainErr || !errors.Is(err, plainErr) {
		t.Fatalf("enrichOpenError(plain error) = %v (%T), want the original error", err, err)
	}
}

func TestOpenWithOptionsPreservesUnrecognizedSQLiteError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	if err := os.WriteFile(dbPath, []byte("this is not a sqlite database"), 0o644); err != nil {
		t.Fatalf("write fixture sqlite: %v", err)
	}
	db, err := OpenWithOptions(Options{Path: dbPath})
	if err == nil {
		_ = db.Close()
		t.Fatal("open garbage sqlite: expected error")
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("error %q (%T) does not carry a *sqlite.Error", err, err)
	}
	if code := sqliteErr.Code(); code != sqlite3.SQLITE_NOTADB {
		t.Fatalf("fixture error code = %d, want SQLITE_NOTADB (%d)", code, sqlite3.SQLITE_NOTADB)
	}
	if message := err.Error(); strings.Contains(message, "temporary directory") || strings.Contains(message, "not writable") {
		t.Fatalf("unrecognized SQLite error was misclassified: %q", message)
	}
}
