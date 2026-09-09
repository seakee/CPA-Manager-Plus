package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompactUsageCompactsWithoutChangingLogicalSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	payload := strings.Repeat("x", 32*1024)
	for index := 1; index <= 160; index++ {
		if _, err := db.Exec(`insert into usage_events (
			event_hash, timestamp_ms, timestamp, model, input_tokens, output_tokens,
			total_tokens, raw_json, created_at_ms
		) values (?, ?, ?, 'gpt-test', 10, 5, 15, ?, ?)`,
			fmt.Sprintf("compact-event-%03d", index),
			int64(index),
			"1970-01-01T00:00:00Z",
			payload,
			int64(index),
		); err != nil {
			t.Fatalf("insert fixture event %d: %v", index, err)
		}
	}
	if _, err := db.Exec(`delete from usage_events where id <= 120`); err != nil {
		t.Fatalf("fragment fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
	beforeDatabaseBytes := fileSize(t, path)
	beforeWALBytes := fileSize(t, path+"-wal")
	beforeSHMBytes := fileSize(t, path+"-shm")
	if beforeSHMBytes != 0 {
		t.Fatalf("closed fixture SHM bytes = %d, want 0", beforeSHMBytes)
	}

	result, err := CompactUsage(context.Background(), path)
	if err != nil {
		t.Fatalf("CompactUsage() error = %v", err)
	}
	if !result.IntegrityVerified {
		t.Fatal("integrity was not verified")
	}
	if result.Summary.UsageEventCount != 40 || result.Summary.UsageEventMaxID != 160 {
		t.Fatalf("summary = %#v", result.Summary)
	}
	if result.Summary.InputTokens != 400 || result.Summary.OutputTokens != 200 || result.Summary.TotalTokens != 600 {
		t.Fatalf("token summary = %#v", result.Summary)
	}
	if result.Before.DatabaseBytes != beforeDatabaseBytes || result.Before.WALBytes != beforeWALBytes || result.Before.SHMBytes != beforeSHMBytes {
		t.Fatalf("before file sizes = %#v, want database=%d wal=%d shm=%d", result.Before, beforeDatabaseBytes, beforeWALBytes, beforeSHMBytes)
	}
	if result.Before.TotalBytes != beforeDatabaseBytes+beforeWALBytes+beforeSHMBytes {
		t.Fatalf("before total bytes = %d", result.Before.TotalBytes)
	}
	if result.After.DatabaseBytes > result.Before.DatabaseBytes {
		t.Fatalf("database grew during compact: before=%d after=%d", result.Before.DatabaseBytes, result.After.DatabaseBytes)
	}
	if result.After.FreelistCount != 0 {
		t.Fatalf("post-VACUUM freelist = %d, want 0", result.After.FreelistCount)
	}
	afterDatabaseBytes := fileSize(t, path)
	afterWALBytes := fileSize(t, path+"-wal")
	afterSHMBytes := fileSize(t, path+"-shm")
	if result.After.DatabaseBytes != afterDatabaseBytes || result.After.WALBytes != afterWALBytes || result.After.SHMBytes != afterSHMBytes {
		t.Fatalf("after file sizes = %#v, want database=%d wal=%d shm=%d", result.After, afterDatabaseBytes, afterWALBytes, afterSHMBytes)
	}
	if result.After.TotalBytes != afterDatabaseBytes+afterWALBytes+afterSHMBytes {
		t.Fatalf("after total bytes = %d", result.After.TotalBytes)
	}

	verifyDB, err := sql.Open("sqlite", maintenanceDataSourceName(path))
	if err != nil {
		t.Fatalf("reopen compacted database: %v", err)
	}
	defer verifyDB.Close()
	var count, inputTokens, outputTokens, totalTokens int64
	if err := verifyDB.QueryRow(`select count(*), sum(input_tokens), sum(output_tokens), sum(total_tokens) from usage_events`).Scan(
		&count,
		&inputTokens,
		&outputTokens,
		&totalTokens,
	); err != nil {
		t.Fatalf("verify logical summary: %v", err)
	}
	if count != 40 || inputTokens != 400 || outputTokens != 200 || totalTokens != 600 {
		t.Fatalf("logical data changed: count=%d tokens=%d/%d/%d", count, inputTokens, outputTokens, totalTokens)
	}
}

const compactUsageCrashWALEnv = "CPAMP_COMPACT_USAGE_CRASH_WAL_FIXTURE"

func TestCompactUsageCheckpointsCrashRecoveredWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	command := exec.Command(os.Args[0], "-test.run=^TestCompactUsageCrashWALFixtureProcess$")
	command.Env = append(os.Environ(), compactUsageCrashWALEnv+"="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create crash-recovered WAL fixture: %v\n%s", err, output)
	}
	beforeWALBytes := fileSize(t, path+"-wal")
	if beforeWALBytes <= 0 {
		t.Fatalf("crash-recovered WAL bytes = %d, want non-zero", beforeWALBytes)
	}

	result, err := CompactUsage(context.Background(), path)
	if err != nil {
		t.Fatalf("CompactUsage() error = %v", err)
	}
	if result.Before.WALBytes != beforeWALBytes {
		t.Fatalf("before WAL bytes = %d, want %d", result.Before.WALBytes, beforeWALBytes)
	}
	if result.After.WALBytes != 0 || fileSize(t, path+"-wal") != 0 {
		t.Fatalf("WAL was not truncated: result=%d file=%d", result.After.WALBytes, fileSize(t, path+"-wal"))
	}
	if result.Summary.UsageEventCount != 32 || result.Summary.TotalTokens != 480 {
		t.Fatalf("crash-recovered logical summary = %#v", result.Summary)
	}
}

func TestCompactUsageCrashWALFixtureProcess(t *testing.T) {
	path := os.Getenv(compactUsageCrashWALEnv)
	if path == "" {
		t.Skip("crash-recovered WAL helper process only")
	}
	db, err := OpenWithOptions(Options{Path: path, MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatalf("open crash WAL fixture: %v", err)
	}
	if _, err := db.Exec(`pragma wal_autocheckpoint = 0`); err != nil {
		t.Fatalf("disable WAL autocheckpoint: %v", err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin crash WAL fixture: %v", err)
	}
	for index := 1; index <= 32; index++ {
		if _, err := tx.Exec(`insert into usage_events (
			event_hash, timestamp_ms, timestamp, model, input_tokens, output_tokens,
			total_tokens, raw_json, created_at_ms
		) values (?, ?, '1970-01-01T00:00:00Z', 'gpt-test', 10, 5, 15, ?, ?)`,
			fmt.Sprintf("crash-wal-event-%03d", index),
			int64(index),
			strings.Repeat("w", 4096),
			int64(index),
		); err != nil {
			_ = tx.Rollback()
			t.Fatalf("insert crash WAL event %d: %v", index, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit crash WAL fixture: %v", err)
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() <= 0 {
		t.Fatalf("crash WAL fixture size: info=%v err=%v", info, err)
	}
	os.Exit(0)
}

func TestReadPageStatsReportsMainDatabaseAndCompanionFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`insert into settings (key, value, updated_at_ms) values ('page-stats', 'fixture', 1)`); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	stats, err := ReadPageStats(context.Background(), db)
	if err != nil {
		t.Fatalf("ReadPageStats() error = %v", err)
	}
	databaseBytes := fileSize(t, path)
	walBytes := fileSize(t, path+"-wal")
	shmBytes := fileSize(t, path+"-shm")
	if stats.DatabaseBytes != databaseBytes || stats.WALBytes != walBytes || stats.SHMBytes != shmBytes {
		t.Fatalf("file sizes = %#v, want database=%d wal=%d shm=%d", stats, databaseBytes, walBytes, shmBytes)
	}
	if stats.DatabaseBytes <= 0 || stats.TotalBytes != databaseBytes+walBytes+shmBytes {
		t.Fatalf("total file size = %#v", stats)
	}
}

func TestReadPageStatsKeepsFileSizesZeroForInMemoryDatabase(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory fixture: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(`create table page_stats_fixture (id integer primary key)`); err != nil {
		t.Fatalf("create in-memory fixture: %v", err)
	}

	stats, err := ReadPageStats(context.Background(), db)
	if err != nil {
		t.Fatalf("ReadPageStats() error = %v", err)
	}
	if stats.PageSize <= 0 || stats.PageCount <= 0 {
		t.Fatalf("page stats = %#v", stats)
	}
	if stats.DatabaseBytes != 0 || stats.WALBytes != 0 || stats.SHMBytes != 0 || stats.TotalBytes != 0 {
		t.Fatalf("in-memory file sizes = %#v, want zero", stats)
	}
}

func TestCompactUsageRejectsActiveSQLiteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin held transaction: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`insert into settings (key, value, updated_at_ms) values ('compact-lock', 'held', 1)`); err != nil {
		t.Fatalf("hold write lock: %v", err)
	}

	_, err = CompactUsage(context.Background(), path)
	if !errors.Is(err, ErrMaintenanceBusy) {
		t.Fatalf("CompactUsage() error = %v, want ErrMaintenanceBusy", err)
	}
}

func TestCompactUsageRejectsMaintenanceAndStateMachineBlockers(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *sql.DB)
	}{
		{
			name: "stale maintenance lock",
			setup: func(t *testing.T, db *sql.DB) {
				insertArchiveRun(t, db, "completed-run", "manual", "completed")
				if _, err := db.Exec(`insert into usage_maintenance_locks (
					name, run_id, operation, acquired_at_ms, updated_at_ms
				) values ('usage_archive', 'completed-run', 'deleting', 1, 1)`); err != nil {
					t.Fatalf("insert maintenance lock: %v", err)
				}
			},
		},
		{
			name: "archiving stage",
			setup: func(t *testing.T, db *sql.DB) {
				insertArchiveRun(t, db, "archiving-run", "manual", "archiving")
			},
		},
		{
			name: "verifying stage",
			setup: func(t *testing.T, db *sql.DB) {
				insertArchiveRun(t, db, "verifying-run", "retention", "verifying")
			},
		},
		{
			name: "deleting stage",
			setup: func(t *testing.T, db *sql.DB) {
				insertArchiveRun(t, db, "deleting-run", "manual", "deleting")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "usage.sqlite")
			db, err := Open(path)
			if err != nil {
				t.Fatalf("open fixture: %v", err)
			}
			test.setup(t, db)
			if err := db.Close(); err != nil {
				t.Fatalf("close fixture: %v", err)
			}

			_, err = CompactUsage(context.Background(), path)
			if !errors.Is(err, ErrMaintenanceBlocked) {
				t.Fatalf("CompactUsage() error = %v, want ErrMaintenanceBlocked", err)
			}
		})
	}
}

func TestCompactUsageAllowsAndPreservesPendingDerivedMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if _, err := db.Exec(`update usage_data_migrations set status = 'running',
		last_event_id = 7, target_event_id = 11, processed_rows = 5
		where name = 'usage_cache_accounting_v2'`); err != nil {
		t.Fatalf("set migration state: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	if _, err := CompactUsage(context.Background(), path); err != nil {
		t.Fatalf("CompactUsage() error = %v", err)
	}
	verifyDB, err := sql.Open("sqlite", maintenanceDataSourceName(path))
	if err != nil {
		t.Fatalf("reopen compacted database: %v", err)
	}
	defer verifyDB.Close()
	var status string
	var lastEventID, targetEventID, processedRows int64
	if err := verifyDB.QueryRow(`select status, last_event_id, target_event_id, processed_rows
		from usage_data_migrations where name = 'usage_cache_accounting_v2'`).Scan(
		&status,
		&lastEventID,
		&targetEventID,
		&processedRows,
	); err != nil {
		t.Fatalf("read migration state: %v", err)
	}
	if status != "running" || lastEventID != 7 || targetEventID != 11 || processedRows != 5 {
		t.Fatalf("migration state changed: status=%q last=%d target=%d processed=%d", status, lastEventID, targetEventID, processedRows)
	}
}

func TestCompactUsageSummarizesArchiveEventReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	insertArchiveRun(t, db, "deleted-run", "manual", "completed")
	if _, err := db.Exec(`insert into usage_archive_segments (
		run_id, sequence, status, file_name, first_event_id, last_event_id,
		min_timestamp_ms, max_timestamp_ms, event_count, uncompressed_bytes,
		compressed_bytes, content_sha256, event_hash_digest, created_at_ms, verified_at_ms
	) values ('deleted-run', 1, 'verified', 'deleted-run/segment.jsonl.gz', 1, 1,
		1, 1, 1, 1, 1, 'content', 'events', 1, 1)`); err != nil {
		t.Fatalf("insert archive segment: %v", err)
	}
	if _, err := db.Exec(`insert into usage_event_identity_ledger (
		event_hash, raw_event_id, timestamp_ms, bucket_ms,
		aggregate_schema_version, aggregate_structure_revision,
		first_seen_at_ms, updated_at_ms
	) values ('deleted-event', null, 1, 0, 1, 'revision', 1, 1)`); err != nil {
		t.Fatalf("insert identity ledger: %v", err)
	}
	if _, err := db.Exec(`insert into usage_archive_event_refs (
		event_hash, run_id, segment_sequence, raw_event_id, timestamp_ms,
		archived_at_ms, raw_deleted_at_ms
	) values ('deleted-event', 'deleted-run', 1, 1, 1, 1, 2)`); err != nil {
		t.Fatalf("insert archive event reference: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	result, err := CompactUsage(context.Background(), path)
	if err != nil {
		t.Fatalf("CompactUsage() error = %v", err)
	}
	if result.Summary.IdentityLedgerCount != 1 || result.Summary.ArchiveRunCount != 1 ||
		result.Summary.ArchiveSegmentCount != 1 || result.Summary.ArchiveEventRefCount != 1 ||
		result.Summary.RawDeletedEventCount != 1 {
		t.Fatalf("archive summary = %#v", result.Summary)
	}
}

func TestCompactUsageAllowsInactiveArchiveRuns(t *testing.T) {
	for _, status := range []string{"previewed", "archived", "verified", "failed"} {
		t.Run(status, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "usage.sqlite")
			db, err := Open(path)
			if err != nil {
				t.Fatalf("open fixture: %v", err)
			}
			insertArchiveRun(t, db, status+"-run", "manual", status)
			if err := db.Close(); err != nil {
				t.Fatalf("close fixture: %v", err)
			}

			if _, err := CompactUsage(context.Background(), path); err != nil {
				t.Fatalf("CompactUsage() error = %v", err)
			}
		})
	}
}

func TestCompactUsageRejectsForeignKeyCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	corruptDB, err := sql.Open("sqlite", maintenanceDataSourceName(path))
	if err != nil {
		t.Fatalf("open corruption fixture: %v", err)
	}
	if _, err := corruptDB.Exec(`pragma foreign_keys = off`); err != nil {
		t.Fatalf("disable foreign keys: %v", err)
	}
	if _, err := corruptDB.Exec(`insert into usage_archive_segments (
		run_id, sequence, status, file_name, first_event_id, last_event_id,
		min_timestamp_ms, max_timestamp_ms, event_count, uncompressed_bytes,
		compressed_bytes, content_sha256, event_hash_digest, created_at_ms
	) values ('missing-run', 1, 'published', 'missing-run/segment.jsonl.gz', 1, 1,
		1, 1, 1, 1, 1, 'content', 'events', 1)`); err != nil {
		t.Fatalf("insert foreign key violation: %v", err)
	}
	if err := corruptDB.Close(); err != nil {
		t.Fatalf("close corruption fixture: %v", err)
	}

	_, err = CompactUsage(context.Background(), path)
	if !errors.Is(err, ErrMaintenanceIntegrity) {
		t.Fatalf("CompactUsage() error = %v, want ErrMaintenanceIntegrity", err)
	}
}

func TestCompactUsageRejectsInvalidDatabasePaths(t *testing.T) {
	dir := t.TempDir()
	emptyPath := filepath.Join(dir, "empty.sqlite")
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatalf("write empty fixture: %v", err)
	}
	nonCPAMPPath := filepath.Join(dir, "other.sqlite")
	nonCPAMP, err := sql.Open("sqlite", dataSourceName(nonCPAMPPath))
	if err != nil {
		t.Fatalf("open non-CPAMP fixture: %v", err)
	}
	if _, err := nonCPAMP.Exec(`create table unrelated (id integer primary key)`); err != nil {
		t.Fatalf("create non-CPAMP fixture: %v", err)
	}
	if err := nonCPAMP.Close(); err != nil {
		t.Fatalf("close non-CPAMP fixture: %v", err)
	}

	for _, path := range []string{filepath.Join(dir, "missing.sqlite"), emptyPath, dir, nonCPAMPPath} {
		if _, err := CompactUsage(context.Background(), path); !errors.Is(err, ErrMaintenanceInvalidDatabase) {
			t.Errorf("CompactUsage(%q) error = %v, want ErrMaintenanceInvalidDatabase", path, err)
		}
	}
}

func insertArchiveRun(t *testing.T, db *sql.DB, id, mode, status string) {
	t.Helper()
	if _, err := db.Exec(`insert into usage_archive_runs (
		id, mode, schema_version, format, status, cutoff_timestamp_ms,
		target_event_id, event_count, created_at_ms, updated_at_ms
	) values (?, ?, 1, 'gzip-jsonl-v1', ?, 1, 1, 1, 1, 1)`, id, mode, status); err != nil {
		t.Fatalf("insert archive run: %v", err)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}
