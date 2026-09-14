package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoricalRawDeletionExists(t *testing.T) {
	t.Run("table does not exist", func(t *testing.T) {
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.sqlite"))
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		defer db.Close()

		exists, err := HistoricalRawDeletionExists(db)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if exists {
			t.Fatalf("expected false, got true")
		}

		if err := ensureCompleteRawSourceForDerivedRebuild(db, "test reason"); err != nil {
			t.Fatalf("ensureCompleteRawSourceForDerivedRebuild unexpected error: %v", err)
		}
	})

	t.Run("table exists but all raw_deleted_at_ms are null", func(t *testing.T) {
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.sqlite"))
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		defer db.Close()

		if _, err := db.Exec(`create table usage_archive_event_refs (
			id integer primary key,
			raw_deleted_at_ms integer
		)`); err != nil {
			t.Fatalf("create table: %v", err)
		}

		if _, err := db.Exec(`insert into usage_archive_event_refs (raw_deleted_at_ms) values (null), (null)`); err != nil {
			t.Fatalf("insert rows: %v", err)
		}

		exists, err := HistoricalRawDeletionExists(db)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if exists {
			t.Fatalf("expected false, got true")
		}

		if err := ensureCompleteRawSourceForDerivedRebuild(db, "test reason"); err != nil {
			t.Fatalf("ensureCompleteRawSourceForDerivedRebuild unexpected error: %v", err)
		}
	})

	t.Run("table exists and has raw_deleted_at_ms not null", func(t *testing.T) {
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.sqlite"))
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		defer db.Close()

		if _, err := db.Exec(`create table usage_archive_event_refs (
			id integer primary key,
			raw_deleted_at_ms integer
		)`); err != nil {
			t.Fatalf("create table: %v", err)
		}

		if _, err := db.Exec(`insert into usage_archive_event_refs (raw_deleted_at_ms) values (null), (1700000000000)`); err != nil {
			t.Fatalf("insert rows: %v", err)
		}

		exists, err := HistoricalRawDeletionExists(db)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !exists {
			t.Fatalf("expected true, got false")
		}

		err = ensureCompleteRawSourceForDerivedRebuild(db, "account and pricing rollups")
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "historical raw usage events have been archived and deleted") {
			t.Fatalf("unexpected error message: %v", err)
		}
		if !strings.Contains(err.Error(), "account and pricing rollups") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}
