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
)

var (
	ErrMaintenanceInvalidDatabase = errors.New("invalid CPA Manager Plus database")
	ErrMaintenanceBlocked         = errors.New("usage maintenance is blocked")
	ErrMaintenanceBusy            = errors.New("usage database is busy; stop every Manager Server process before compacting")
	ErrMaintenanceIntegrity       = errors.New("usage database integrity check failed")
)

type PageStats struct {
	PageSize         int64 `json:"page_size"`
	PageCount        int64 `json:"page_count"`
	FreelistCount    int64 `json:"freelist_count"`
	ReclaimableBytes int64 `json:"reclaimable_bytes"`
	DatabaseBytes    int64 `json:"database_bytes"`
	WALBytes         int64 `json:"wal_bytes"`
	SHMBytes         int64 `json:"shm_bytes"`
	TotalBytes       int64 `json:"total_bytes"`
}

type DataSummary struct {
	UsageEventCount      int64 `json:"usage_event_count"`
	UsageEventMaxID      int64 `json:"usage_event_max_id"`
	IdentityLedgerCount  int64 `json:"identity_ledger_count"`
	ArchiveSegmentCount  int64 `json:"archive_segment_count"`
	ArchiveEventRefCount int64 `json:"archive_event_ref_count"`
	RawDeletedEventCount int64 `json:"raw_deleted_event_count"`
	ArchiveRunCount      int64 `json:"archive_run_count"`
	InputTokens          int64 `json:"input_tokens"`
	OutputTokens         int64 `json:"output_tokens"`
	TotalTokens          int64 `json:"total_tokens"`
}

type CheckpointStats struct {
	Busy               int64 `json:"busy"`
	LogFrames          int64 `json:"log_frames"`
	CheckpointedFrames int64 `json:"checkpointed_frames"`
}

type CompactResult struct {
	DatabasePath      string          `json:"database_path"`
	Before            PageStats       `json:"before"`
	After             PageStats       `json:"after"`
	Summary           DataSummary     `json:"summary"`
	PreCheckpoint     CheckpointStats `json:"pre_checkpoint"`
	PostCheckpoint    CheckpointStats `json:"post_checkpoint"`
	ReclaimedBytes    int64           `json:"reclaimed_bytes"`
	IntegrityVerified bool            `json:"integrity_verified"`
}

// CompactUsage performs the offline physical compaction step only. The caller
// must hold the process-level database lock for the resolved path for the full
// call. CompactUsage never archives or deletes logical usage records and never
// removes WAL, SHM, or archive files itself.
func CompactUsage(ctx context.Context, path string) (CompactResult, error) {
	dbPath, err := ResolveMaintenancePath(path)
	if err != nil {
		return CompactResult{}, err
	}
	beforeFiles, err := readMaintenanceFileStats(dbPath)
	if err != nil {
		return CompactResult{}, err
	}

	db, err := sql.Open("sqlite", maintenanceDataSourceName(dbPath))
	if err != nil {
		return CompactResult{}, fmt.Errorf("open usage database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	dbOpen := true
	defer func() {
		if dbOpen {
			_ = db.Close()
		}
	}()

	conn, err := db.Conn(ctx)
	if err != nil {
		return CompactResult{}, classifyMaintenanceError("open dedicated SQLite connection", err)
	}
	connOpen := true
	defer func() {
		if connOpen {
			_ = conn.Close()
		}
	}()

	if _, err := conn.ExecContext(ctx, `pragma busy_timeout = 0`); err != nil {
		return CompactResult{}, classifyMaintenanceError("disable SQLite busy wait", err)
	}
	var lockingMode string
	if err := conn.QueryRowContext(ctx, `pragma locking_mode = EXCLUSIVE`).Scan(&lockingMode); err != nil {
		return CompactResult{}, classifyMaintenanceError("request exclusive SQLite access", err)
	}
	if !strings.EqualFold(strings.TrimSpace(lockingMode), "exclusive") {
		return CompactResult{}, fmt.Errorf("%w: SQLite refused exclusive locking mode", ErrMaintenanceBusy)
	}

	if _, err := conn.ExecContext(ctx, `begin exclusive`); err != nil {
		return CompactResult{}, classifyMaintenanceError("acquire exclusive SQLite lock", err)
	}
	transactionOpen := true
	rollback := func() {
		if transactionOpen {
			_, _ = conn.ExecContext(context.Background(), `rollback`)
			transactionOpen = false
		}
	}
	defer rollback()

	if err := validateCPAMPDatabase(ctx, conn); err != nil {
		return CompactResult{}, err
	}
	if err := validateMaintenanceReadiness(ctx, conn); err != nil {
		return CompactResult{}, err
	}
	if err := runSingleValueCheck(ctx, conn, "quick_check", `pragma quick_check`); err != nil {
		return CompactResult{}, err
	}
	if err := runForeignKeyCheck(ctx, conn); err != nil {
		return CompactResult{}, err
	}

	before, err := readPageStats(ctx, conn, "")
	if err != nil {
		return CompactResult{}, err
	}
	mergeMaintenanceFileStats(&before, beforeFiles)
	beforeSummary, err := readDataSummary(ctx, conn)
	if err != nil {
		return CompactResult{}, err
	}
	rollback()

	preCheckpoint, err := truncateCheckpoint(ctx, conn)
	if err != nil {
		return CompactResult{}, err
	}
	if _, err := conn.ExecContext(ctx, `vacuum`); err != nil {
		return CompactResult{}, classifyMaintenanceError("VACUUM usage database", err)
	}
	postCheckpoint, err := truncateCheckpoint(ctx, conn)
	if err != nil {
		return CompactResult{}, err
	}
	if err := runSingleValueCheck(ctx, conn, "integrity_check", `pragma integrity_check`); err != nil {
		return CompactResult{}, err
	}
	if err := runForeignKeyCheck(ctx, conn); err != nil {
		return CompactResult{}, err
	}
	after, err := readPageStats(ctx, conn, "")
	if err != nil {
		return CompactResult{}, err
	}
	afterSummary, err := readDataSummary(ctx, conn)
	if err != nil {
		return CompactResult{}, err
	}
	if beforeSummary != afterSummary {
		return CompactResult{}, fmt.Errorf("%w: logical usage summary changed during compaction", ErrMaintenanceIntegrity)
	}
	if err := conn.Close(); err != nil {
		connOpen = false
		return CompactResult{}, classifyMaintenanceError("close dedicated SQLite connection", err)
	}
	connOpen = false
	if err := db.Close(); err != nil {
		dbOpen = false
		return CompactResult{}, classifyMaintenanceError("close usage database", err)
	}
	dbOpen = false
	afterFiles, err := readMaintenanceFileStats(dbPath)
	if err != nil {
		return CompactResult{}, err
	}
	mergeMaintenanceFileStats(&after, afterFiles)

	reclaimed := before.TotalBytes - after.TotalBytes
	if reclaimed < 0 {
		reclaimed = 0
	}
	return CompactResult{
		DatabasePath:      dbPath,
		Before:            before,
		After:             after,
		Summary:           afterSummary,
		PreCheckpoint:     preCheckpoint,
		PostCheckpoint:    postCheckpoint,
		ReclaimedBytes:    reclaimed,
		IntegrityVerified: true,
	}, nil
}

// ReadPageStats exposes the read-only page/freelist snapshot used by the
// management API. It does not checkpoint, lock, vacuum, or mutate the DB.
func ReadPageStats(ctx context.Context, db *sql.DB) (PageStats, error) {
	if db == nil {
		return PageStats{}, errors.New("SQLite database is not configured")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return PageStats{}, classifyMaintenanceError("open dedicated SQLite connection", err)
	}
	defer conn.Close()

	dbPath, err := readMainDatabasePath(ctx, conn)
	if err != nil {
		return PageStats{}, err
	}
	return readPageStats(ctx, conn, dbPath)
}

// ResolveMaintenancePath resolves an existing non-empty database file without
// opening or mutating it. Callers that coordinate offline ownership can use the
// returned absolute path before acquiring the process-level database lock.
func ResolveMaintenancePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("%w: database path is empty", ErrMaintenanceInvalidDatabase)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: resolve database path: %v", ErrMaintenanceInvalidDatabase, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: database does not exist: %s", ErrMaintenanceInvalidDatabase, abs)
		}
		return "", fmt.Errorf("%w: inspect database: %v", ErrMaintenanceInvalidDatabase, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: database path is not a regular file: %s", ErrMaintenanceInvalidDatabase, abs)
	}
	if info.Size() == 0 {
		return "", fmt.Errorf("%w: database file is empty: %s", ErrMaintenanceInvalidDatabase, abs)
	}
	return filepath.Clean(abs), nil
}

func maintenanceDataSourceName(path string) string {
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := &url.URL{Scheme: "file", Path: uriPath}
	query := dsn.Query()
	query.Set("mode", "rw")
	query.Add("_pragma", "busy_timeout(0)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "synchronous(FULL)")
	dsn.RawQuery = query.Encode()
	return dsn.String()
}

func validateCPAMPDatabase(ctx context.Context, conn *sql.Conn) error {
	required := []string{
		"settings",
		"usage_events",
		"usage_event_identity_ledger",
		"usage_archive_runs",
		"usage_archive_segments",
		"usage_archive_event_refs",
		"usage_maintenance_locks",
		"usage_data_migrations",
	}
	for _, table := range required {
		var found string
		err := conn.QueryRowContext(ctx, `select name from sqlite_master where type = 'table' and name = ?`, table).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: required table %q is missing", ErrMaintenanceInvalidDatabase, table)
		}
		if err != nil {
			return classifyMaintenanceError("inspect CPA Manager Plus schema", err)
		}
	}
	return nil
}

func validateMaintenanceReadiness(ctx context.Context, conn *sql.Conn) error {
	var lockName, runID, operation string
	err := conn.QueryRowContext(ctx, `select name, run_id, operation from usage_maintenance_locks order by name limit 1`).Scan(
		&lockName,
		&runID,
		&operation,
	)
	if err == nil {
		return fmt.Errorf("%w: lock %q is held by archive run %q (%s)", ErrMaintenanceBlocked, lockName, runID, operation)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return classifyMaintenanceError("inspect usage maintenance locks", err)
	}

	var archiveID, mode, status string
	err = conn.QueryRowContext(ctx, `select id, mode, status from usage_archive_runs
		where status in ('archiving', 'verifying', 'deleting')
		order by created_at_ms asc limit 1`).Scan(&archiveID, &mode, &status)
	if err == nil {
		return fmt.Errorf("%w: active %s archive stage for run %q is %s", ErrMaintenanceBlocked, mode, archiveID, status)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return classifyMaintenanceError("inspect usage archive runs", err)
	}

	return nil
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readMainDatabasePath(ctx context.Context, queryer queryRower) (string, error) {
	rows, err := queryer.QueryContext(ctx, `pragma database_list`)
	if err != nil {
		return "", classifyMaintenanceError("locate SQLite main database", err)
	}
	defer rows.Close()

	for rows.Next() {
		var sequence int
		var name, path string
		if err := rows.Scan(&sequence, &name, &path); err != nil {
			return "", fmt.Errorf("scan SQLite database list: %w", err)
		}
		if name == "main" {
			return strings.TrimSpace(path), nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", classifyMaintenanceError("read SQLite database list", err)
	}
	return "", errors.New("SQLite main database is not attached")
}

func readPageStats(ctx context.Context, queryer queryRower, dbPath string) (PageStats, error) {
	var stats PageStats
	for _, item := range []struct {
		query string
		dest  *int64
	}{
		{query: `pragma page_size`, dest: &stats.PageSize},
		{query: `pragma page_count`, dest: &stats.PageCount},
		{query: `pragma freelist_count`, dest: &stats.FreelistCount},
	} {
		if err := queryer.QueryRowContext(ctx, item.query).Scan(item.dest); err != nil {
			return PageStats{}, classifyMaintenanceError("read SQLite page statistics", err)
		}
	}
	stats.ReclaimableBytes = stats.PageSize * stats.FreelistCount
	if dbPath != "" {
		var err error
		if stats.DatabaseBytes, err = maintenanceFileSize(dbPath); err != nil {
			return PageStats{}, err
		}
		if stats.WALBytes, err = maintenanceFileSize(dbPath + "-wal"); err != nil {
			return PageStats{}, err
		}
		if stats.SHMBytes, err = maintenanceFileSize(dbPath + "-shm"); err != nil {
			return PageStats{}, err
		}
		stats.TotalBytes = stats.DatabaseBytes + stats.WALBytes + stats.SHMBytes
	}
	return stats, nil
}

func readMaintenanceFileStats(dbPath string) (PageStats, error) {
	var stats PageStats
	var err error
	if stats.DatabaseBytes, err = maintenanceFileSize(dbPath); err != nil {
		return PageStats{}, err
	}
	if stats.WALBytes, err = maintenanceFileSize(dbPath + "-wal"); err != nil {
		return PageStats{}, err
	}
	if stats.SHMBytes, err = maintenanceFileSize(dbPath + "-shm"); err != nil {
		return PageStats{}, err
	}
	stats.TotalBytes = stats.DatabaseBytes + stats.WALBytes + stats.SHMBytes
	return stats, nil
}

func mergeMaintenanceFileStats(stats *PageStats, files PageStats) {
	stats.DatabaseBytes = files.DatabaseBytes
	stats.WALBytes = files.WALBytes
	stats.SHMBytes = files.SHMBytes
	stats.TotalBytes = files.TotalBytes
}

func maintenanceFileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("inspect SQLite file %s: %w", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("SQLite companion path is not a regular file: %s", filepath.Base(path))
	}
	return info.Size(), nil
}

func readDataSummary(ctx context.Context, conn *sql.Conn) (DataSummary, error) {
	var summary DataSummary
	if err := conn.QueryRowContext(ctx, `select
		count(*), coalesce(max(id), 0), coalesce(sum(input_tokens), 0),
		coalesce(sum(output_tokens), 0), coalesce(sum(total_tokens), 0)
		from usage_events`).Scan(
		&summary.UsageEventCount,
		&summary.UsageEventMaxID,
		&summary.InputTokens,
		&summary.OutputTokens,
		&summary.TotalTokens,
	); err != nil {
		return DataSummary{}, classifyMaintenanceError("summarize usage events", err)
	}
	if err := conn.QueryRowContext(ctx, `select count(*) from usage_event_identity_ledger`).Scan(&summary.IdentityLedgerCount); err != nil {
		return DataSummary{}, classifyMaintenanceError("summarize usage identity ledger", err)
	}
	if err := conn.QueryRowContext(ctx, `select count(*) from usage_archive_segments`).Scan(&summary.ArchiveSegmentCount); err != nil {
		return DataSummary{}, classifyMaintenanceError("summarize usage archive segments", err)
	}
	if err := conn.QueryRowContext(ctx, `select count(*),
		coalesce(sum(case when raw_deleted_at_ms is not null then 1 else 0 end), 0)
		from usage_archive_event_refs`).Scan(&summary.ArchiveEventRefCount, &summary.RawDeletedEventCount); err != nil {
		return DataSummary{}, classifyMaintenanceError("summarize usage archive event references", err)
	}
	if err := conn.QueryRowContext(ctx, `select count(*) from usage_archive_runs`).Scan(&summary.ArchiveRunCount); err != nil {
		return DataSummary{}, classifyMaintenanceError("summarize usage archive runs", err)
	}
	return summary, nil
}

func truncateCheckpoint(ctx context.Context, conn *sql.Conn) (CheckpointStats, error) {
	var stats CheckpointStats
	if err := conn.QueryRowContext(ctx, `pragma wal_checkpoint(TRUNCATE)`).Scan(
		&stats.Busy,
		&stats.LogFrames,
		&stats.CheckpointedFrames,
	); err != nil {
		return CheckpointStats{}, classifyMaintenanceError("truncate SQLite WAL checkpoint", err)
	}
	if stats.Busy != 0 {
		return CheckpointStats{}, fmt.Errorf("%w: WAL checkpoint reported %d busy reader or writer", ErrMaintenanceBusy, stats.Busy)
	}
	return stats, nil
}

func runSingleValueCheck(ctx context.Context, conn *sql.Conn, name, query string) error {
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return classifyMaintenanceError("run SQLite "+name, err)
	}
	defer rows.Close()
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return fmt.Errorf("scan SQLite %s result: %w", name, err)
		}
		if !strings.EqualFold(strings.TrimSpace(result), "ok") {
			return fmt.Errorf("%w: %s returned %s", ErrMaintenanceIntegrity, name, result)
		}
	}
	if err := rows.Err(); err != nil {
		return classifyMaintenanceError("read SQLite "+name, err)
	}
	return nil
}

func runForeignKeyCheck(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `pragma foreign_key_check`)
	if err != nil {
		return classifyMaintenanceError("run SQLite foreign_key_check", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var rowID sql.NullInt64
		var foreignKeyID int64
		if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			return fmt.Errorf("scan SQLite foreign_key_check result: %w", err)
		}
		return fmt.Errorf(
			"%w: foreign key violation in table %q row %d referencing %q constraint %d",
			ErrMaintenanceIntegrity,
			table,
			rowID.Int64,
			parent,
			foreignKeyID,
		)
	}
	if err := rows.Err(); err != nil {
		return classifyMaintenanceError("read SQLite foreign_key_check", err)
	}
	return nil
}

func classifyMaintenanceError(operation string, err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked") ||
		strings.Contains(message, "sqlite_busy") ||
		strings.Contains(message, "sqlite_locked") {
		return fmt.Errorf("%w: %s: %v", ErrMaintenanceBusy, operation, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
