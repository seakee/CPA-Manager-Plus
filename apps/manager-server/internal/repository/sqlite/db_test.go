package sqlite

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"
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
	dbPath := filepath.Join(t.TempDir(), "usage ? #.sqlite")
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
