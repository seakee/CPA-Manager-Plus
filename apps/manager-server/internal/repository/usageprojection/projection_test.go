package usageprojection

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

func TestSearchIndexLikePatternKeepsExactFallbackBoundary(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		wantPattern string
		wantOK      bool
	}{
		{name: "ordinary substring", query: " Trace-ABC ", wantPattern: "%trace-abc%", wantOK: true},
		{name: "three unicode characters", query: "中文测", wantPattern: "%中文测%", wantOK: true},
		{name: "one character", query: "a", wantOK: false},
		{name: "two characters", query: "ab", wantOK: false},
		{name: "two unicode characters", query: "中文", wantOK: false},
		{name: "percent wildcard", query: "trace%", wantOK: false},
		{name: "underscore wildcard", query: "trace_", wantOK: false},
		{name: "projection separator", query: "a\x1fb", wantOK: false},
		{name: "control character", query: "a\nb", wantOK: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pattern, ok := SearchIndexLikePattern(test.query)
			if ok != test.wantOK || pattern != test.wantPattern {
				t.Fatalf("SearchIndexLikePattern(%q) = %q, %v; want %q, %v", test.query, pattern, ok, test.wantPattern, test.wantOK)
			}
		})
	}
}

func TestVerifyRetainedEdgeTx(t *testing.T) {
	ctx := context.Background()
	setupDB := func(t *testing.T) *sql.DB {
		t.Helper()
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("open sqlite: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		schema := `
			create table usage_monitoring_rollup_state (
				rollup_name text primary key,
				schema_version int,
				structure_revision text,
				status text,
				coverage_event_id int
			);
			create table usage_archive_event_refs (
				raw_event_id int,
				raw_deleted_at_ms int,
				timestamp_ms int
			);
			create table usage_monitoring_event_projection_v1 (
				event_id int
			);
		`
		if _, err := db.Exec(schema); err != nil {
			t.Fatalf("create schema: %v", err)
		}
		return db
	}

	currentRevision := usageidentity.MonitoringProjectionStructureRevision()

	t.Run("missing projection state", func(t *testing.T) {
		db := setupDB(t)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = VerifyRetainedEdgeTx(ctx, tx, 1000, 2000)
		if !errors.Is(err, ErrRetainedCoverageIncomplete) {
			t.Fatalf("expected ErrRetainedCoverageIncomplete, got %v", err)
		}
	})

	t.Run("incompatible schema version", func(t *testing.T) {
		db := setupDB(t)
		if _, err := db.Exec(`insert into usage_monitoring_rollup_state values ('projection_v1', 0, ?, 'ready', 10)`, currentRevision); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = VerifyRetainedEdgeTx(ctx, tx, 1000, 2000)
		if !errors.Is(err, ErrRetainedCoverageIncomplete) {
			t.Fatalf("expected ErrRetainedCoverageIncomplete, got %v", err)
		}
	})

	t.Run("incompatible structure revision", func(t *testing.T) {
		db := setupDB(t)
		if _, err := db.Exec(`insert into usage_monitoring_rollup_state values ('projection_v1', 1, 'obsolete', 'ready', 10)`); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = VerifyRetainedEdgeTx(ctx, tx, 1000, 2000)
		if !errors.Is(err, ErrRetainedCoverageIncomplete) {
			t.Fatalf("expected ErrRetainedCoverageIncomplete, got %v", err)
		}
	})

	t.Run("clearing status", func(t *testing.T) {
		db := setupDB(t)
		if _, err := db.Exec(`insert into usage_monitoring_rollup_state values ('projection_v1', 1, ?, 'clearing', 10)`, currentRevision); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = VerifyRetainedEdgeTx(ctx, tx, 1000, 2000)
		if !errors.Is(err, ErrRetainedCoverageIncomplete) {
			t.Fatalf("expected ErrRetainedCoverageIncomplete, got %v", err)
		}
	})

	t.Run("missing deleted edge event in projection", func(t *testing.T) {
		db := setupDB(t)
		if _, err := db.Exec(`insert into usage_monitoring_rollup_state values ('projection_v1', 1, ?, 'ready', 10)`, currentRevision); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`insert into usage_archive_event_refs values (5, 1500, 1500)`); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = VerifyRetainedEdgeTx(ctx, tx, 1000, 2000)
		if !errors.Is(err, ErrRetainedCoverageIncomplete) {
			t.Fatalf("expected ErrRetainedCoverageIncomplete, got %v", err)
		}
	})

	t.Run("edge event beyond coverage ID", func(t *testing.T) {
		db := setupDB(t)
		if _, err := db.Exec(`insert into usage_monitoring_rollup_state values ('projection_v1', 1, ?, 'ready', 5)`, currentRevision); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`insert into usage_archive_event_refs values (10, 1500, 1500)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`insert into usage_monitoring_event_projection_v1 values (10)`); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = VerifyRetainedEdgeTx(ctx, tx, 1000, 2000)
		if !errors.Is(err, ErrRetainedCoverageIncomplete) {
			t.Fatalf("expected ErrRetainedCoverageIncomplete, got %v", err)
		}
	})

	t.Run("complete retained edge", func(t *testing.T) {
		db := setupDB(t)
		if _, err := db.Exec(`insert into usage_monitoring_rollup_state values ('projection_v1', 1, ?, 'ready', 10)`, currentRevision); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`insert into usage_archive_event_refs values (5, 1500, 1500)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`insert into usage_monitoring_event_projection_v1 values (5)`); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = VerifyRetainedEdgeTx(ctx, tx, 1000, 2000)
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	})

	t.Run("real SQL query error does not return ErrRetainedCoverageIncomplete", func(t *testing.T) {
		db := setupDB(t)
		if _, err := db.Exec(`drop table usage_monitoring_rollup_state`); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = VerifyRetainedEdgeTx(ctx, tx, 1000, 2000)
		if err == nil {
			t.Fatal("expected error on dropped table, got nil")
		}
		if errors.Is(err, ErrRetainedCoverageIncomplete) {
			t.Fatalf("SQL query error must not be classified as ErrRetainedCoverageIncomplete: %v", err)
		}
	})
}
