package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func Open(path string) (*sql.DB, error) {
	return OpenWithOptions(Options{Path: path})
}

func OpenWithOptions(options Options) (*sql.DB, error) {
	dbPath, err := filepath.Abs(options.Path)
	if err != nil {
		return nil, err
	}
	if err := ensureDatabaseParentDirectory(filepath.Dir(dbPath)); err != nil {
		return nil, err
	}
	precreated := preparePrivateDatabaseFile(dbPath)
	db, err := sql.Open("sqlite", dataSourceName(dbPath))
	if err != nil {
		if precreated {
			removeEmptyPrecreatedFile(dbPath)
		}
		return nil, enrichOpenError(dbPath, err)
	}
	db.SetMaxOpenConns(options.maxOpenConns())
	db.SetMaxIdleConns(options.maxIdleConns())
	db.SetConnMaxIdleTime(options.connMaxIdleTime())
	if err := Migrate(db); err != nil {
		_ = db.Close()
		if precreated {
			removeEmptyPrecreatedFile(dbPath)
		}
		return nil, enrichOpenError(dbPath, err)
	}
	if err := enforcePrivateDatabaseFiles(dbPath); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func ensureDatabaseParentDirectory(dir string) error {
	if _, err := os.Lstat(dir); err == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

func preparePrivateDatabaseFile(dbPath string) bool {
	info, err := os.Lstat(dbPath)
	if errors.Is(err, os.ErrNotExist) {
		f, createErr := os.OpenFile(dbPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			return false
		}
		_ = f.Close()
		_ = os.Chmod(dbPath, 0o600)
		return true
	}
	if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o200 != 0 {
		_ = os.Chmod(dbPath, 0o600)
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if sideInfo, sideErr := os.Lstat(dbPath + suffix); sideErr == nil && sideInfo.Mode().IsRegular() {
				_ = os.Chmod(dbPath+suffix, 0o600)
			}
		}
	}
	return false
}

func removeEmptyPrecreatedFile(dbPath string) {
	if info, err := os.Lstat(dbPath); err == nil && info.Mode().IsRegular() && info.Size() == 0 {
		_ = os.Remove(dbPath)
	}
}

func enforcePrivateDatabaseFiles(dbPath string) error {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		candidate := dbPath + suffix
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			if err := os.Chmod(candidate, 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

// enrichOpenError adds actionable context to the few SQLite result codes known
// to fail startup in hardened containers. Other errors pass through unchanged
// and the original error always stays in the chain.
func enrichOpenError(dbPath string, err error) error {
	if err == nil {
		return nil
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return err
	}
	if message := openDiagnostic(sqliteErr.Code(), dbPath); message != "" {
		return fmt.Errorf("%s: %w", message, err)
	}
	return err
}

// openDiagnostic maps a SQLite result code to startup context, or returns an
// empty string when the code needs no extra context.
func openDiagnostic(code int, dbPath string) string {
	switch {
	case code == sqlite3.SQLITE_IOERR_GETTEMPPATH:
		return "SQLite temporary directory is unavailable; configure a writable operating-system temporary directory"
	case code == sqlite3.SQLITE_READONLY:
		return fmt.Sprintf("SQLite database is not writable at %q; check file ownership, permissions, and whether the database volume is writable", dbPath)
	default:
		return ""
	}
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
