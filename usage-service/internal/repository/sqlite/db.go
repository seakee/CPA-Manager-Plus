package sqlite

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func Open(path string) (*sql.DB, error) {
	return OpenWithOptions(Options{Path: path})
}

func OpenWithOptions(options Options) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(options.Path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", options.Path)
	if err != nil {
		return nil, err
	}
	if err := Migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
