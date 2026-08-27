package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

func Open(path string) (*sql.DB, error) {
	return OpenWithOptions(Options{Path: path})
}

func OpenWithOptions(options Options) (*sql.DB, error) {
	dbPath, err := filepath.Abs(options.Path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}
	options.Path = dbPath
	db, err := OpenUnmigratedWithOptions(options)
	if err != nil {
		return nil, err
	}
	if err := MigrateWithOptions(db, options); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func OpenUnmigratedWithOptions(options Options) (*sql.DB, error) {
	if err := options.validate(); err != nil {
		return nil, err
	}
	dbPath, err := filepath.Abs(options.Path)
	if err != nil {
		return nil, err
	}
	dsn := dataSourceName(dbPath)
	if options.hasCustomDataSourceName() {
		dsn, err = customDataSourceName(options.DataSourceName, dbPath)
		if err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(options.maxOpenConns())
	db.SetMaxIdleConns(options.maxIdleConns())
	db.SetConnMaxIdleTime(options.connMaxIdleTime())
	if options.hasCustomDataSourceName() {
		if err := validateCustomConnectionPragmas(context.Background(), db, options); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return db, nil
}

func dataSourceName(path string) string {
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := &url.URL{
		Scheme: "file",
		Path:   uriPath,
	}
	query := dsn.Query()
	query.Add("_txlock", "immediate")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "synchronous(FULL)")
	dsn.RawQuery = query.Encode()
	return dsn.String()
}

func customDataSourceName(rawURL string, path string) (string, error) {
	dsn, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", fmt.Errorf("parse SQLite data source name: %w", sanitizedURLParseError(err))
	}
	if !strings.EqualFold(dsn.Scheme, "file") || dsn.Opaque != "" || dsn.Host != "" || dsn.User != nil || dsn.Fragment != "" {
		return "", fmt.Errorf("custom SQLite data source must be a local hierarchical file URL")
	}
	query, err := url.ParseQuery(dsn.RawQuery)
	if err != nil {
		return "", fmt.Errorf("parse SQLite data source query: %w", sanitizedURLParseError(err))
	}
	if err := validateCustomTransactionLock(query); err != nil {
		return "", err
	}
	if err := validatePersistentDataSourceOptions(query); err != nil {
		return "", err
	}
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn.Path = uriPath
	dsn.RawPath = ""
	return dsn.String(), nil
}

func validateCustomTransactionLock(query url.Values) error {
	values := query["_txlock"]
	if len(values) != 1 || !strings.EqualFold(strings.TrimSpace(values[0]), "immediate") {
		return fmt.Errorf("custom SQLite data source must set _txlock=immediate")
	}
	return nil
}

func validatePersistentDataSourceOptions(query url.Values) error {
	for key, values := range query {
		switch strings.ToLower(key) {
		case "mode":
			for _, value := range values {
				if strings.EqualFold(strings.TrimSpace(value), "memory") {
					return fmt.Errorf("custom SQLite data source must reference a persistent file")
				}
			}
		case "immutable", "nolock":
			for _, value := range values {
				if !explicitFalse(value) {
					return fmt.Errorf("custom SQLite data source must not enable %s", strings.ToLower(key))
				}
			}
		case "vfs":
			return fmt.Errorf("custom SQLite data source must not select a custom VFS")
		}
	}
	return nil
}

func sanitizedURLParseError(err error) error {
	var urlError *url.Error
	if errors.As(err, &urlError) && urlError.Err != nil {
		return urlError.Err
	}
	return err
}

func explicitFalse(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no", "off":
		return true
	default:
		return false
	}
}

func validateCustomConnectionPragmas(ctx context.Context, db *sql.DB, options Options) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open SQLite connection for URL validation: %w", err)
	}
	defer conn.Close()

	var journalMode string
	if err := conn.QueryRowContext(ctx, `pragma journal_mode`).Scan(&journalMode); err != nil {
		return fmt.Errorf("read SQLite journal mode: %w", err)
	}
	journalMode = strings.ToLower(strings.TrimSpace(journalMode))
	if expected := strings.ToLower(strings.TrimSpace(options.ExpectedJournalMode)); journalMode != expected {
		return fmt.Errorf("SQLite database URL applied journal mode %q, want %q", journalMode, expected)
	}

	for _, check := range []struct {
		name  string
		query string
		want  int
	}{
		{name: "foreign_keys", query: `pragma foreign_keys`, want: 1},
		{name: "synchronous", query: `pragma synchronous`, want: options.ExpectedSynchronous},
		{name: "busy_timeout", query: `pragma busy_timeout`, want: options.ExpectedBusyTimeout},
	} {
		var got int
		if err := conn.QueryRowContext(ctx, check.query).Scan(&got); err != nil {
			return fmt.Errorf("read SQLite %s pragma: %w", check.name, err)
		}
		if got != check.want {
			return fmt.Errorf("SQLite database URL applied %s=%d, want %d", check.name, got, check.want)
		}
	}
	return nil
}

func JournalMode(ctx context.Context, db *sql.DB) (string, error) {
	if db == nil {
		return "", fmt.Errorf("read SQLite journal mode: database is nil")
	}
	var mode string
	if err := db.QueryRowContext(ctx, `pragma journal_mode`).Scan(&mode); err != nil {
		return "", fmt.Errorf("read SQLite journal mode: %w", err)
	}
	return strings.ToLower(strings.TrimSpace(mode)), nil
}
