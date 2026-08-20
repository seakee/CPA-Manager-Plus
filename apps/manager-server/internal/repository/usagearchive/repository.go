package usagearchive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/datamigration"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageaggregate"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagemonitoring"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagepricing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagerollup"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	SchemaVersion     = usage.ArchiveSchemaVersion
	FormatGzipJSONLV1 = "gzip-jsonl-v1"

	StatusPreviewed = "previewed"
	StatusArchiving = "archiving"
	StatusArchived  = "archived"
	StatusVerifying = "verifying"
	StatusVerified  = "verified"
	StatusDeleting  = "deleting"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"

	SegmentStatusPublished = "published"
	SegmentStatusVerified  = "verified"

	MaintenanceLockName = "usage_archive"
	RunModeManual       = "manual"
	RunModeRetention    = "retention"
	derivedStatusReady  = "ready"
)

var (
	ErrNotFound           = errors.New("usage archive run not found")
	ErrNoEvents           = errors.New("no usage events are eligible for archive")
	ErrInvalidState       = errors.New("usage archive run is in an invalid state")
	ErrMaintenanceLocked  = errors.New("usage maintenance is already active")
	ErrCoverageIncomplete = errors.New("usage archive coverage is incomplete")
)

type Preview struct {
	CutoffTimestampMS int64 `json:"cutoff_timestamp_ms"`
	TargetEventID     int64 `json:"target_event_id"`
	EventCount        int64 `json:"event_count"`
	EstimatedBytes    int64 `json:"estimated_bytes"`
	MinTimestampMS    int64 `json:"min_timestamp_ms,omitempty"`
	MaxTimestampMS    int64 `json:"max_timestamp_ms,omitempty"`
}

type Run struct {
	ID                        string `json:"id"`
	Mode                      string `json:"mode"`
	SchemaVersion             int    `json:"schema_version"`
	Format                    string `json:"format"`
	Status                    string `json:"status"`
	ResumeStatus              string `json:"resume_status,omitempty"`
	RequestedStage            string `json:"requested_stage,omitempty"`
	CutoffTimestampMS         int64  `json:"cutoff_timestamp_ms"`
	TargetEventID             int64  `json:"target_event_id"`
	EventCount                int64  `json:"event_count"`
	EstimatedBytes            int64  `json:"estimated_bytes"`
	LastArchivedEventID       int64  `json:"last_archived_event_id"`
	ArchivedEventCount        int64  `json:"archived_event_count"`
	ArchivedUncompressedBytes int64  `json:"archived_uncompressed_bytes"`
	ArchivedCompressedBytes   int64  `json:"archived_compressed_bytes"`
	ArchiveDigest             string `json:"archive_digest,omitempty"`
	ManifestFile              string `json:"manifest_file,omitempty"`
	ManifestSHA256            string `json:"manifest_sha256,omitempty"`
	LastDeletedEventID        int64  `json:"last_deleted_event_id"`
	DeletedEventCount         int64  `json:"deleted_event_count"`
	CreatedAtMS               int64  `json:"created_at_ms"`
	UpdatedAtMS               int64  `json:"updated_at_ms"`
	StartedAtMS               int64  `json:"started_at_ms,omitempty"`
	ArchivedAtMS              int64  `json:"archived_at_ms,omitempty"`
	VerifiedAtMS              int64  `json:"verified_at_ms,omitempty"`
	DeleteStartedAtMS         int64  `json:"delete_started_at_ms,omitempty"`
	CompletedAtMS             int64  `json:"completed_at_ms,omitempty"`
	LastError                 string `json:"last_error,omitempty"`
}

type RunListFilter struct {
	Status            string
	Mode              string
	Limit             int
	BeforeCreatedAtMS int64
	BeforeID          string
}

type RunListResult struct {
	Runs         []Run
	Total        int64
	StatusCounts map[string]int64
	HasMore      bool
}

type Segment struct {
	RunID             string `json:"run_id"`
	Sequence          int    `json:"sequence"`
	Status            string `json:"status"`
	FileName          string `json:"file_name"`
	FirstEventID      int64  `json:"first_event_id"`
	LastEventID       int64  `json:"last_event_id"`
	MinTimestampMS    int64  `json:"min_timestamp_ms"`
	MaxTimestampMS    int64  `json:"max_timestamp_ms"`
	EventCount        int64  `json:"event_count"`
	UncompressedBytes int64  `json:"uncompressed_bytes"`
	CompressedBytes   int64  `json:"compressed_bytes"`
	ContentSHA256     string `json:"content_sha256"`
	EventHashDigest   string `json:"event_hash_digest"`
	CreatedAtMS       int64  `json:"created_at_ms"`
	VerifiedAtMS      int64  `json:"verified_at_ms,omitempty"`
}

type Record struct {
	EventID     int64
	EventHash   string
	TimestampMS int64
	Payload     []byte
}

type RecordRef struct {
	EventID   int64
	EventHash string
}

type DeleteBatchResult struct {
	Deleted   int
	LastID    int64
	Completed bool
	Run       Run
}

type RawCoverage struct {
	FromTimestampMS       int64 `json:"from_timestamp_ms"`
	ToTimestampMS         int64 `json:"to_timestamp_ms"`
	RawEventCount         int64 `json:"raw_event_count"`
	RawDeletedEventCount  int64 `json:"raw_deleted_event_count"`
	MinDeletedTimestampMS int64 `json:"min_deleted_timestamp_ms,omitempty"`
	MaxDeletedTimestampMS int64 `json:"max_deleted_timestamp_ms,omitempty"`
}

type MaintenanceLock struct {
	Name         string `json:"name"`
	RunID        string `json:"run_id"`
	Operation    string `json:"operation"`
	AcquiredAtMS int64  `json:"acquired_at_ms"`
	UpdatedAtMS  int64  `json:"updated_at_ms"`
}

type MaintenanceCounts struct {
	RawEventCount         int64 `json:"raw_event_count"`
	RawMinTimestampMS     int64 `json:"raw_min_timestamp_ms"`
	RawMaxTimestampMS     int64 `json:"raw_max_timestamp_ms"`
	RawArchivedEventCount int64 `json:"raw_archived_event_count"`
	RawDeletedEventCount  int64 `json:"raw_deleted_event_count"`
}

type Repository struct {
	db *sql.DB
}

func New(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Preview(ctx context.Context, cutoffTimestampMS int64) (Preview, error) {
	if cutoffTimestampMS <= 0 {
		return Preview{}, fmt.Errorf("cutoff timestamp must be greater than zero")
	}
	return previewQuery(ctx, r.db, cutoffTimestampMS)
}

func (r *Repository) CreateRun(ctx context.Context, id string, cutoffTimestampMS, nowMS int64) (Run, error) {
	return r.createRun(ctx, id, RunModeManual, cutoffTimestampMS, nowMS)
}

func (r *Repository) CreateRetentionRun(ctx context.Context, id string, cutoffTimestampMS, nowMS int64) (Run, error) {
	return r.createRun(ctx, id, RunModeRetention, cutoffTimestampMS, nowMS)
}

func (r *Repository) createRun(ctx context.Context, id, mode string, cutoffTimestampMS, nowMS int64) (Run, error) {
	id = strings.TrimSpace(id)
	if id == "" || (mode != RunModeManual && mode != RunModeRetention) || cutoffTimestampMS <= 0 || nowMS <= 0 {
		return Run{}, fmt.Errorf("invalid usage archive run input")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `update usage_archive_runs set updated_at_ms = updated_at_ms where id = ?`, id); err != nil {
		return Run{}, err
	}
	var activeID string
	err = tx.QueryRowContext(ctx, `select id from usage_archive_runs
		where status not in (?, ?)
			and (mode = ? or status not in (?, ?))
		order by created_at_ms asc limit 1`,
		StatusCompleted,
		StatusCancelled,
		RunModeRetention,
		StatusArchived,
		StatusVerified,
	).Scan(&activeID)
	if err == nil {
		return Run{}, fmt.Errorf("%w: run %s", ErrMaintenanceLocked, activeID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Run{}, err
	}
	preview, err := previewQuery(ctx, tx, cutoffTimestampMS)
	if err != nil {
		return Run{}, err
	}
	if preview.EventCount == 0 {
		return Run{}, ErrNoEvents
	}
	if _, err := tx.ExecContext(ctx, `insert into usage_archive_runs (
		id, mode, schema_version, format, status, cutoff_timestamp_ms, target_event_id,
		event_count, estimated_bytes, created_at_ms, updated_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id,
		mode,
		SchemaVersion,
		FormatGzipJSONLV1,
		StatusPreviewed,
		cutoffTimestampMS,
		preview.TargetEventID,
		preview.EventCount,
		preview.EstimatedBytes,
		nowMS,
		nowMS,
	); err != nil {
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return r.Run(ctx, id)
}

func (r *Repository) Run(ctx context.Context, id string) (Run, error) {
	return runQuery(ctx, r.db, id)
}

func (r *Repository) ListRuns(ctx context.Context, filter RunListFilter) (RunListResult, error) {
	if filter.Limit <= 0 {
		filter.Limit = 20
	}
	where, args := archiveRunListPredicates(filter.Status, filter.Mode)
	var total int64
	if err := r.db.QueryRowContext(ctx, `select count(*) from usage_archive_runs where `+where, args...).Scan(&total); err != nil {
		return RunListResult{}, err
	}
	countWhere, countArgs := archiveRunListPredicates("", filter.Mode)
	countRows, err := r.db.QueryContext(ctx, `select status, count(*) from usage_archive_runs where `+countWhere+` group by status`, countArgs...)
	if err != nil {
		return RunListResult{}, err
	}
	statusCounts := make(map[string]int64)
	for countRows.Next() {
		var status string
		var count int64
		if err := countRows.Scan(&status, &count); err != nil {
			_ = countRows.Close()
			return RunListResult{}, err
		}
		statusCounts[status] = count
	}
	if err := countRows.Err(); err != nil {
		_ = countRows.Close()
		return RunListResult{}, err
	}
	if err := countRows.Close(); err != nil {
		return RunListResult{}, err
	}
	if filter.BeforeCreatedAtMS > 0 && strings.TrimSpace(filter.BeforeID) != "" {
		where += ` and (created_at_ms < ? or (created_at_ms = ? and id < ?))`
		args = append(args, filter.BeforeCreatedAtMS, filter.BeforeCreatedAtMS, filter.BeforeID)
	}
	args = append(args, filter.Limit+1)
	rows, err := r.db.QueryContext(ctx, `select
		id, mode, schema_version, format, status, resume_status, requested_stage, cutoff_timestamp_ms,
		target_event_id, event_count, estimated_bytes, last_archived_event_id,
		archived_event_count, archived_uncompressed_bytes, archived_compressed_bytes,
		archive_digest, manifest_file, manifest_sha256, last_deleted_event_id,
		deleted_event_count, created_at_ms, updated_at_ms, started_at_ms, archived_at_ms,
		verified_at_ms, delete_started_at_ms, completed_at_ms, last_error
	from usage_archive_runs where `+where+` order by created_at_ms desc, id desc limit ?`, args...)
	if err != nil {
		return RunListResult{}, err
	}
	defer rows.Close()
	runs := make([]Run, 0, min(filter.Limit+1, 101))
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return RunListResult{}, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return RunListResult{}, err
	}
	hasMore := len(runs) > filter.Limit
	if hasMore {
		runs = runs[:filter.Limit]
	}
	return RunListResult{Runs: runs, Total: total, StatusCounts: statusCounts, HasMore: hasMore}, nil
}

func archiveRunListPredicates(status, mode string) (string, []any) {
	predicates := []string{"1 = 1"}
	args := make([]any, 0, 2)
	if status = strings.TrimSpace(status); status != "" {
		predicates = append(predicates, "status = ?")
		args = append(args, status)
	}
	if mode = strings.TrimSpace(mode); mode != "" {
		predicates = append(predicates, "mode = ?")
		args = append(args, mode)
	}
	return strings.Join(predicates, " and "), args
}

func (r *Repository) MaintenanceLock(ctx context.Context) (MaintenanceLock, bool, error) {
	var lock MaintenanceLock
	err := r.db.QueryRowContext(ctx, `select name, run_id, operation, acquired_at_ms, updated_at_ms
		from usage_maintenance_locks order by acquired_at_ms asc limit 1`).Scan(
		&lock.Name,
		&lock.RunID,
		&lock.Operation,
		&lock.AcquiredAtMS,
		&lock.UpdatedAtMS,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return MaintenanceLock{}, false, nil
	}
	if err != nil {
		return MaintenanceLock{}, false, err
	}
	return lock, true, nil
}

func (r *Repository) MaintenanceCounts(ctx context.Context) (MaintenanceCounts, error) {
	var counts MaintenanceCounts
	if err := r.db.QueryRowContext(ctx, `select
		count(*), coalesce(min(timestamp_ms), 0), coalesce(max(timestamp_ms), 0)
		from usage_events`).Scan(
		&counts.RawEventCount,
		&counts.RawMinTimestampMS,
		&counts.RawMaxTimestampMS,
	); err != nil {
		return MaintenanceCounts{}, err
	}
	if err := r.db.QueryRowContext(ctx, `select
		coalesce(sum(case when raw_deleted_at_ms is null then 1 else 0 end), 0),
		coalesce(sum(case when raw_deleted_at_ms is not null then 1 else 0 end), 0)
		from usage_archive_event_refs`).Scan(
		&counts.RawArchivedEventCount,
		&counts.RawDeletedEventCount,
	); err != nil {
		return MaintenanceCounts{}, err
	}
	return counts, nil
}

func (r *Repository) ActiveRun(ctx context.Context) (Run, bool, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `select id from usage_archive_runs
		where status not in (?, ?)
			and (mode = ? or status not in (?, ?))
		order by created_at_ms asc limit 1`,
		StatusCompleted,
		StatusCancelled,
		RunModeRetention,
		StatusArchived,
		StatusVerified,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	run, err := r.Run(ctx, id)
	if err != nil {
		return Run{}, false, err
	}
	return run, true, nil
}

func (r *Repository) RequestStage(ctx context.Context, runID, stage string, nowMS int64) (Run, bool, error) {
	if !validRequestedStage(stage) || nowMS <= 0 {
		return Run{}, false, fmt.Errorf("%w: invalid requested archive stage", ErrInvalidState)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `update usage_archive_runs set updated_at_ms = updated_at_ms where id = ?`, runID); err != nil {
		return Run{}, false, err
	}
	run, err := runQuery(ctx, tx, runID)
	if err != nil {
		return Run{}, false, err
	}
	if run.RequestedStage != "" && requestedStageSatisfied(run, run.RequestedStage) {
		if _, err := tx.ExecContext(ctx, `update usage_archive_runs set requested_stage = null where id = ? and requested_stage = ?`, runID, run.RequestedStage); err != nil {
			return Run{}, false, err
		}
		run.RequestedStage = ""
	}
	if requestedStageSatisfied(run, stage) {
		if err := tx.Commit(); err != nil {
			return Run{}, false, err
		}
		return run, false, nil
	}
	if run.RequestedStage != "" {
		if run.RequestedStage != stage {
			return Run{}, false, fmt.Errorf("%w: stage %s is already requested", ErrInvalidState, run.RequestedStage)
		}
		if err := tx.Commit(); err != nil {
			return Run{}, false, err
		}
		return run, false, nil
	}
	if !canRequestStage(run, stage) {
		return Run{}, false, fmt.Errorf("%w: cannot request %s from %s", ErrInvalidState, stage, run.Status)
	}
	if _, err := tx.ExecContext(ctx, `update usage_archive_runs set requested_stage = ?, updated_at_ms = ? where id = ?`, stage, nowMS, runID); err != nil {
		return Run{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, false, err
	}
	run.RequestedStage = stage
	run.UpdatedAtMS = nowMS
	return run, true, nil
}

func (r *Repository) ClearRequestedStage(ctx context.Context, runID, stage string) error {
	if !validRequestedStage(stage) {
		return fmt.Errorf("%w: invalid requested archive stage", ErrInvalidState)
	}
	_, err := r.db.ExecContext(ctx, `update usage_archive_runs set requested_stage = null where id = ? and requested_stage = ?`, runID, stage)
	return err
}

func (r *Repository) RecoverRequestedStages(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `update usage_archive_runs set requested_stage = status
		where requested_stage is null and status in (?, ?, ?)`,
		StatusArchiving,
		StatusVerifying,
		StatusDeleting,
	)
	return err
}

func (r *Repository) NextRequestedRun(ctx context.Context) (Run, bool, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `select id from usage_archive_runs
		where requested_stage in (?, ?, ?)
		order by updated_at_ms asc, created_at_ms asc, id asc limit 1`,
		StatusArchiving,
		StatusVerifying,
		StatusDeleting,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	run, err := r.Run(ctx, id)
	if err != nil {
		return Run{}, false, err
	}
	return run, true, nil
}

func validRequestedStage(stage string) bool {
	switch stage {
	case StatusArchiving, StatusVerifying, StatusDeleting:
		return true
	default:
		return false
	}
}

func canRequestStage(run Run, stage string) bool {
	if run.Status == StatusFailed {
		return run.ResumeStatus == stage
	}
	switch stage {
	case StatusArchiving:
		return run.Status == StatusPreviewed || run.Status == StatusArchiving
	case StatusVerifying:
		return run.Status == StatusArchived || run.Status == StatusVerifying
	case StatusDeleting:
		return run.Status == StatusVerified || run.Status == StatusDeleting
	default:
		return false
	}
}

func requestedStageSatisfied(run Run, stage string) bool {
	switch stage {
	case StatusArchiving:
		return run.Status == StatusArchived || run.Status == StatusVerifying || run.Status == StatusVerified ||
			run.Status == StatusDeleting || run.Status == StatusCompleted
	case StatusVerifying:
		return run.Status == StatusVerified || run.Status == StatusDeleting || run.Status == StatusCompleted
	case StatusDeleting:
		return run.Status == StatusCompleted
	default:
		return false
	}
}

func (r *Repository) Segments(ctx context.Context, runID string) ([]Segment, error) {
	rows, err := r.db.QueryContext(ctx, `select
		run_id, sequence, status, file_name, first_event_id, last_event_id,
		min_timestamp_ms, max_timestamp_ms, event_count, uncompressed_bytes,
		compressed_bytes, content_sha256, event_hash_digest, created_at_ms, verified_at_ms
	from usage_archive_segments where run_id = ? order by sequence`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	segments := make([]Segment, 0)
	for rows.Next() {
		segment, err := scanSegment(rows)
		if err != nil {
			return nil, err
		}
		segments = append(segments, segment)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return segments, nil
}

func (r *Repository) RawCoverage(ctx context.Context, fromTimestampMS, toTimestampMS int64) (RawCoverage, error) {
	if fromTimestampMS <= 0 || toTimestampMS <= fromTimestampMS {
		return RawCoverage{}, fmt.Errorf("invalid usage coverage range")
	}
	coverage := RawCoverage{
		FromTimestampMS: fromTimestampMS,
		ToTimestampMS:   toTimestampMS,
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RawCoverage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.QueryRowContext(ctx, `select
		count(*),
		coalesce(min(timestamp_ms), 0),
		coalesce(max(timestamp_ms), 0)
	from usage_archive_event_refs
	where timestamp_ms >= ? and timestamp_ms < ? and raw_deleted_at_ms is not null`, fromTimestampMS, toTimestampMS).Scan(
		&coverage.RawDeletedEventCount,
		&coverage.MinDeletedTimestampMS,
		&coverage.MaxDeletedTimestampMS,
	); err != nil {
		return RawCoverage{}, err
	}
	if coverage.RawDeletedEventCount > 0 {
		if err := tx.QueryRowContext(ctx, `select count(*) from usage_events
			where timestamp_ms >= ? and timestamp_ms < ?`, fromTimestampMS, toTimestampMS).Scan(&coverage.RawEventCount); err != nil {
			return RawCoverage{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return RawCoverage{}, err
	}
	return coverage, nil
}

func (r *Repository) RawEventCount(ctx context.Context, fromTimestampMS, toTimestampMS int64) (int64, error) {
	if fromTimestampMS <= 0 || toTimestampMS <= fromTimestampMS {
		return 0, fmt.Errorf("invalid usage raw event count range")
	}
	var count int64
	if err := r.db.QueryRowContext(ctx, `select count(*) from usage_events
		where timestamp_ms >= ? and timestamp_ms < ?`, fromTimestampMS, toTimestampMS).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (r *Repository) BeginArchive(ctx context.Context, runID string, nowMS int64) (Run, error) {
	return r.beginStage(ctx, runID, StatusArchiving, []string{StatusPreviewed, StatusArchiving}, nowMS)
}

func (r *Repository) BeginVerification(ctx context.Context, runID string, nowMS int64) (Run, error) {
	return r.beginStage(ctx, runID, StatusVerifying, []string{StatusArchived, StatusVerifying}, nowMS)
}

func (r *Repository) BeginDelete(ctx context.Context, runID string, nowMS int64) (Run, error) {
	return r.beginStage(ctx, runID, StatusDeleting, []string{StatusVerified, StatusDeleting}, nowMS)
}

func (r *Repository) beginStage(ctx context.Context, runID string, stage string, allowed []string, nowMS int64) (Run, error) {
	if nowMS <= 0 {
		return Run{}, fmt.Errorf("nowMS must be greater than zero")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `update usage_archive_runs set updated_at_ms = updated_at_ms where id = ?`, runID); err != nil {
		return Run{}, err
	}
	run, err := runQuery(ctx, tx, runID)
	if err != nil {
		return Run{}, err
	}
	valid := containsStatus(allowed, run.Status)
	if run.Status == StatusFailed && run.ResumeStatus == stage {
		valid = true
	}
	if !valid {
		return Run{}, fmt.Errorf("%w: cannot enter %s from %s", ErrInvalidState, stage, run.Status)
	}
	if err := validateRunContract(run); err != nil {
		return Run{}, err
	}
	if stage == StatusDeleting {
		if err := validateDeleteCoverage(ctx, tx, run); err != nil {
			return Run{}, err
		}
	}
	if err := acquireLock(ctx, tx, runID, stage, nowMS); err != nil {
		return Run{}, err
	}
	startedColumn := "started_at_ms"
	if stage == StatusDeleting {
		startedColumn = "delete_started_at_ms"
	}
	statement := fmt.Sprintf(`update usage_archive_runs set
		status = ?, resume_status = null, last_error = null, updated_at_ms = ?,
		%s = coalesce(%s, ?)
	where id = ?`, startedColumn, startedColumn)
	if _, err := tx.ExecContext(ctx, statement, stage, nowMS, nowMS, runID); err != nil {
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return r.Run(ctx, runID)
}

func (r *Repository) Records(ctx context.Context, runID string, afterEventID int64, limit int, maxBytes int64) ([]Record, error) {
	if limit <= 0 {
		limit = 10_000
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("archive record byte limit must be greater than zero")
	}
	run, err := r.Run(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Status != StatusArchiving {
		return nil, fmt.Errorf("%w: records require archiving state", ErrInvalidState)
	}
	if err := validateRunContract(run); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `select
		e.id,
		e.event_hash,
		e.timestamp_ms,
		`+archiveRecordExpression+` as payload
	from usage_events e
	where e.id > ? and e.id <= ? and e.timestamp_ms < ?
		and not exists (
			select 1 from usage_archive_event_refs archived
			where archived.event_hash = e.event_hash
		)
	order by e.id asc
	limit ?`, run.SchemaVersion, afterEventID, run.TargetEventID, run.CutoffTimestampMS, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]Record, 0, min(limit, 1024))
	var totalBytes int64
	for rows.Next() {
		var record Record
		if err := rows.Scan(&record.EventID, &record.EventHash, &record.TimestampMS, &record.Payload); err != nil {
			return nil, err
		}
		if len(record.Payload) > usage.MaxJSONLRecordBytes {
			return nil, fmt.Errorf(
				"%w: archive event %d payload is %d bytes",
				usage.ErrJSONLRecordTooLarge,
				record.EventID,
				len(record.Payload),
			)
		}
		recordBytes := int64(len(record.Payload) + 1)
		if len(records) > 0 && totalBytes+recordBytes > maxBytes {
			break
		}
		records = append(records, record)
		totalBytes += recordBytes
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func (r *Repository) RecordSegment(ctx context.Context, runID string, segment Segment, refs []RecordRef, nowMS int64) (Run, error) {
	if nowMS <= 0 || segment.RunID != runID || segment.Sequence <= 0 || segment.EventCount <= 0 ||
		int64(len(refs)) != segment.EventCount || segment.FirstEventID <= 0 || segment.LastEventID < segment.FirstEventID ||
		segment.UncompressedBytes <= 0 || segment.CompressedBytes <= 0 ||
		strings.TrimSpace(segment.ContentSHA256) == "" || strings.TrimSpace(segment.EventHashDigest) == "" {
		return Run{}, fmt.Errorf("invalid usage archive segment")
	}
	if path.IsAbs(segment.FileName) || path.Clean(segment.FileName) != segment.FileName ||
		strings.Count(segment.FileName, "/") != 1 || path.Dir(segment.FileName) != runID ||
		path.Base(segment.FileName) == "." || path.Base(segment.FileName) == ".." {
		return Run{}, fmt.Errorf("invalid usage archive segment file name")
	}
	for index, ref := range refs {
		if ref.EventID <= 0 || strings.TrimSpace(ref.EventHash) == "" ||
			(index > 0 && ref.EventID <= refs[index-1].EventID) {
			return Run{}, fmt.Errorf("invalid usage archive segment event references")
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `update usage_archive_runs set updated_at_ms = updated_at_ms where id = ?`, runID); err != nil {
		return Run{}, err
	}
	run, err := runQuery(ctx, tx, runID)
	if err != nil {
		return Run{}, err
	}
	if run.Status != StatusArchiving {
		return Run{}, fmt.Errorf("%w: segment requires archiving state", ErrInvalidState)
	}
	var nextSequence int
	if err := tx.QueryRowContext(ctx, `select coalesce(max(sequence), 0) + 1 from usage_archive_segments where run_id = ?`, runID).Scan(&nextSequence); err != nil {
		return Run{}, err
	}
	if segment.Sequence != nextSequence || segment.FirstEventID != refs[0].EventID || segment.LastEventID != refs[len(refs)-1].EventID ||
		segment.FirstEventID <= run.LastArchivedEventID || segment.LastEventID > run.TargetEventID ||
		run.ArchivedEventCount+segment.EventCount > run.EventCount {
		return Run{}, fmt.Errorf("%w: archive segment sequence or event range is inconsistent", ErrInvalidState)
	}
	if err := validateSegmentCoverage(ctx, tx, run, segment, refs); err != nil {
		return Run{}, err
	}
	if _, err := tx.ExecContext(ctx, `insert into usage_archive_segments (
		run_id, sequence, status, file_name, first_event_id, last_event_id,
		min_timestamp_ms, max_timestamp_ms, event_count, uncompressed_bytes,
		compressed_bytes, content_sha256, event_hash_digest, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		runID,
		segment.Sequence,
		SegmentStatusPublished,
		segment.FileName,
		segment.FirstEventID,
		segment.LastEventID,
		segment.MinTimestampMS,
		segment.MaxTimestampMS,
		segment.EventCount,
		segment.UncompressedBytes,
		segment.CompressedBytes,
		segment.ContentSHA256,
		segment.EventHashDigest,
		nowMS,
	); err != nil {
		return Run{}, err
	}
	for _, ref := range refs {
		if _, err := tx.ExecContext(ctx, `insert or ignore into usage_event_identity_ledger (
			event_hash, raw_event_id, timestamp_ms, bucket_ms, aggregate_schema_version,
			first_seen_at_ms, updated_at_ms
		)
		select
			e.event_hash,
			e.id,
			e.timestamp_ms,
			e.timestamp_ms - (e.timestamp_ms % 3600000),
			0,
			case when e.created_at_ms > 0 then e.created_at_ms else ? end,
			?
		from usage_events e
		where e.id = ? and e.event_hash = ?`, nowMS, nowMS, ref.EventID, ref.EventHash); err != nil {
			return Run{}, err
		}
		result, err := tx.ExecContext(ctx, `update usage_event_identity_ledger set
			raw_event_id = ?,
			timestamp_ms = (select timestamp_ms from usage_events where id = ? and event_hash = ?),
			bucket_ms = (select timestamp_ms - (timestamp_ms % 3600000)
				from usage_events where id = ? and event_hash = ?),
			updated_at_ms = ?
		where event_hash = ? and (raw_event_id is null or raw_event_id = ?)`,
			ref.EventID,
			ref.EventID,
			ref.EventHash,
			ref.EventID,
			ref.EventHash,
			nowMS,
			ref.EventHash,
			ref.EventID,
		)
		if err != nil {
			return Run{}, err
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return Run{}, err
		}
		if updated != 1 {
			return Run{}, fmt.Errorf("%w: identity ledger missing archive event %d", ErrCoverageIncomplete, ref.EventID)
		}
		result, err = tx.ExecContext(ctx, `insert into usage_archive_event_refs (
			event_hash, run_id, segment_sequence, raw_event_id, timestamp_ms,
			archived_at_ms, raw_deleted_at_ms
		)
		select event_hash, ?, ?, raw_event_id, timestamp_ms, ?, null
		from usage_event_identity_ledger
		where event_hash = ? and raw_event_id = ?`,
			runID,
			segment.Sequence,
			nowMS,
			ref.EventHash,
			ref.EventID,
		)
		if err != nil {
			return Run{}, err
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return Run{}, err
		}
		if inserted != 1 {
			return Run{}, fmt.Errorf("%w: archive event reference missing event %d", ErrCoverageIncomplete, ref.EventID)
		}
	}
	if _, err := tx.ExecContext(ctx, `update usage_archive_runs set
		last_archived_event_id = ?,
		archived_event_count = archived_event_count + ?,
		archived_uncompressed_bytes = archived_uncompressed_bytes + ?,
		archived_compressed_bytes = archived_compressed_bytes + ?,
		updated_at_ms = ?
	where id = ?`,
		segment.LastEventID,
		segment.EventCount,
		segment.UncompressedBytes,
		segment.CompressedBytes,
		nowMS,
		runID,
	); err != nil {
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return r.Run(ctx, runID)
}

func validateSegmentCoverage(ctx context.Context, tx *sql.Tx, run Run, segment Segment, refs []RecordRef) error {
	rows, err := tx.QueryContext(ctx, `select e.id, e.event_hash, e.timestamp_ms
		from usage_events e
		where e.id > ? and e.id <= ? and e.id <= ? and e.timestamp_ms < ?
			and not exists (
				select 1 from usage_archive_event_refs archived
				where archived.event_hash = e.event_hash
			)
		order by e.id asc`,
		run.LastArchivedEventID,
		segment.LastEventID,
		run.TargetEventID,
		run.CutoffTimestampMS,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	var count, minTimestampMS, maxTimestampMS int64
	for rows.Next() {
		var eventID, timestampMS int64
		var eventHash string
		if err := rows.Scan(&eventID, &eventHash, &timestampMS); err != nil {
			return err
		}
		if count >= int64(len(refs)) || refs[count].EventID != eventID || refs[count].EventHash != eventHash {
			return fmt.Errorf("%w: segment does not match the next eligible archive events", ErrCoverageIncomplete)
		}
		if count == 0 {
			minTimestampMS = timestampMS
			maxTimestampMS = timestampMS
		} else {
			minTimestampMS = min(minTimestampMS, timestampMS)
			maxTimestampMS = max(maxTimestampMS, timestampMS)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != int64(len(refs)) {
		return fmt.Errorf("%w: segment does not match the next eligible archive events", ErrCoverageIncomplete)
	}
	if segment.MinTimestampMS != minTimestampMS || segment.MaxTimestampMS != maxTimestampMS {
		return fmt.Errorf("%w: archive segment timestamp range is inconsistent", ErrCoverageIncomplete)
	}
	return nil
}

func (r *Repository) MarkArchived(ctx context.Context, runID, archiveDigest, manifestFile, manifestSHA256 string, nowMS int64) (Run, error) {
	if nowMS <= 0 {
		return Run{}, fmt.Errorf("nowMS must be greater than zero")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `update usage_archive_runs set updated_at_ms = updated_at_ms where id = ?`, runID); err != nil {
		return Run{}, err
	}
	run, err := runQuery(ctx, tx, runID)
	if err != nil {
		return Run{}, err
	}
	if run.Status != StatusArchiving || run.ArchivedEventCount != run.EventCount || run.LastArchivedEventID != run.TargetEventID {
		return Run{}, fmt.Errorf("%w: archive is not complete", ErrCoverageIncomplete)
	}
	if err := validateRunContract(run); err != nil {
		return Run{}, err
	}
	if strings.TrimSpace(archiveDigest) == "" || strings.TrimSpace(manifestFile) == "" || strings.TrimSpace(manifestSHA256) == "" {
		return Run{}, fmt.Errorf("archive manifest metadata is required")
	}
	if _, err := tx.ExecContext(ctx, `update usage_archive_runs set
		status = ?, archive_digest = ?, manifest_file = ?, manifest_sha256 = ?,
		archived_at_ms = ?, updated_at_ms = ?, last_error = null
	where id = ?`, StatusArchived, archiveDigest, manifestFile, manifestSHA256, nowMS, nowMS, runID); err != nil {
		return Run{}, err
	}
	if err := releaseLock(ctx, tx, runID); err != nil {
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return r.Run(ctx, runID)
}

func (r *Repository) MarkVerified(ctx context.Context, runID string, nowMS int64) (Run, error) {
	if nowMS <= 0 {
		return Run{}, fmt.Errorf("nowMS must be greater than zero")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `update usage_archive_runs set updated_at_ms = updated_at_ms where id = ?`, runID); err != nil {
		return Run{}, err
	}
	run, err := runQuery(ctx, tx, runID)
	if err != nil {
		return Run{}, err
	}
	if run.Status != StatusVerifying {
		return Run{}, fmt.Errorf("%w: verification state is required", ErrInvalidState)
	}
	if err := validateRunContract(run); err != nil {
		return Run{}, err
	}
	if err := validateCoverage(ctx, tx, run); err != nil {
		return Run{}, err
	}
	if _, err := tx.ExecContext(ctx, `update usage_archive_segments set
		status = ?, verified_at_ms = ? where run_id = ?`, SegmentStatusVerified, nowMS, runID); err != nil {
		return Run{}, err
	}
	if _, err := tx.ExecContext(ctx, `update usage_archive_runs set
		status = ?, verified_at_ms = ?, updated_at_ms = ?, last_error = null
	where id = ?`, StatusVerified, nowMS, nowMS, runID); err != nil {
		return Run{}, err
	}
	if err := releaseLock(ctx, tx, runID); err != nil {
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return r.Run(ctx, runID)
}

func (r *Repository) DeleteBatch(ctx context.Context, runID string, limit int, nowMS int64) (DeleteBatchResult, error) {
	if limit <= 0 {
		limit = 1_000
	}
	if nowMS <= 0 {
		return DeleteBatchResult{}, fmt.Errorf("nowMS must be greater than zero")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return DeleteBatchResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `update usage_archive_runs set updated_at_ms = updated_at_ms where id = ?`, runID); err != nil {
		return DeleteBatchResult{}, err
	}
	run, err := runQuery(ctx, tx, runID)
	if err != nil {
		return DeleteBatchResult{}, err
	}
	if run.Status != StatusDeleting {
		return DeleteBatchResult{}, fmt.Errorf("%w: deleting state is required", ErrInvalidState)
	}
	if err := validateRunContract(run); err != nil {
		return DeleteBatchResult{}, err
	}
	aggregateState, err := validateCurrentDeleteReadiness(ctx, tx, run)
	if err != nil {
		return DeleteBatchResult{}, err
	}
	rows, err := tx.QueryContext(ctx, `select e.id, archived.event_hash
	from usage_events e
	join usage_archive_event_refs archived
		on archived.raw_event_id = e.id and archived.event_hash = e.event_hash
	join usage_event_identity_ledger l
		on l.event_hash = archived.event_hash and l.raw_event_id = e.id
	where e.id > ? and e.id <= ? and e.timestamp_ms < ?
		and archived.run_id = ? and archived.raw_deleted_at_ms is null
		and l.aggregate_schema_version = ?
		and l.aggregate_structure_revision = ?
	order by e.id asc limit ?`,
		run.LastDeletedEventID,
		run.TargetEventID,
		run.CutoffTimestampMS,
		runID,
		aggregateState.SchemaVersion,
		aggregateState.StructureRevision,
		limit,
	)
	if err != nil {
		return DeleteBatchResult{}, err
	}
	type deleteCandidate struct {
		ID        int64
		EventHash string
	}
	candidates := make([]deleteCandidate, 0, min(limit, 1024))
	for rows.Next() {
		var candidate deleteCandidate
		if err := rows.Scan(&candidate.ID, &candidate.EventHash); err != nil {
			_ = rows.Close()
			return DeleteBatchResult{}, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return DeleteBatchResult{}, err
	}
	if err := rows.Close(); err != nil {
		return DeleteBatchResult{}, err
	}
	for _, candidate := range candidates {
		result, err := tx.ExecContext(ctx, `delete from usage_events
			where id = ? and event_hash = ?`, candidate.ID, candidate.EventHash)
		if err != nil {
			return DeleteBatchResult{}, err
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return DeleteBatchResult{}, err
		}
		if deleted != 1 {
			return DeleteBatchResult{}, fmt.Errorf("%w: raw event %d disappeared during delete", ErrCoverageIncomplete, candidate.ID)
		}
		ledgerResult, err := tx.ExecContext(ctx, `update usage_event_identity_ledger set
			raw_event_id = null, updated_at_ms = ?
			where event_hash = ? and raw_event_id = ?`,
			nowMS,
			candidate.EventHash,
			candidate.ID,
		)
		if err != nil {
			return DeleteBatchResult{}, err
		}
		updatedLedger, err := ledgerResult.RowsAffected()
		if err != nil {
			return DeleteBatchResult{}, err
		}
		if updatedLedger != 1 {
			return DeleteBatchResult{}, fmt.Errorf("%w: ledger event %d disappeared during delete", ErrCoverageIncomplete, candidate.ID)
		}
		refResult, err := tx.ExecContext(ctx, `update usage_archive_event_refs set
			raw_deleted_at_ms = ?
			where run_id = ? and event_hash = ? and raw_event_id = ?
				and raw_deleted_at_ms is null`,
			nowMS,
			runID,
			candidate.EventHash,
			candidate.ID,
		)
		if err != nil {
			return DeleteBatchResult{}, err
		}
		updatedRef, err := refResult.RowsAffected()
		if err != nil {
			return DeleteBatchResult{}, err
		}
		if updatedRef != 1 {
			return DeleteBatchResult{}, fmt.Errorf("%w: archive event reference %d disappeared during delete", ErrCoverageIncomplete, candidate.ID)
		}
	}
	lastID := run.LastDeletedEventID
	if len(candidates) > 0 {
		lastID = candidates[len(candidates)-1].ID
	}
	if _, err := tx.ExecContext(ctx, `update usage_archive_runs set
		last_deleted_event_id = ?, deleted_event_count = deleted_event_count + ?, updated_at_ms = ?
	where id = ?`, lastID, len(candidates), nowMS, runID); err != nil {
		return DeleteBatchResult{}, err
	}
	var remaining, missingRaw int
	if err := tx.QueryRowContext(ctx, `select
		exists(
			select 1 from usage_archive_event_refs
			where run_id = ? and raw_deleted_at_ms is null
			limit 1
		),
		exists(
			select 1
			from usage_archive_event_refs archived
			left join usage_events e
				on e.id = archived.raw_event_id and e.event_hash = archived.event_hash
			where archived.run_id = ? and archived.raw_deleted_at_ms is null
				and e.id is null
			limit 1
		)`, runID, runID).Scan(&remaining, &missingRaw); err != nil {
		return DeleteBatchResult{}, err
	}
	if missingRaw != 0 {
		return DeleteBatchResult{}, fmt.Errorf("%w: an archived raw event disappeared outside bounded delete", ErrCoverageIncomplete)
	}
	if len(candidates) == 0 && remaining != 0 {
		return DeleteBatchResult{}, fmt.Errorf("%w: archived raw events are not deletable", ErrCoverageIncomplete)
	}
	deletedEventCount := run.DeletedEventCount + int64(len(candidates))
	if remaining == 0 && deletedEventCount != run.EventCount {
		return DeleteBatchResult{}, fmt.Errorf(
			"%w: deleted %d of %d archived events",
			ErrCoverageIncomplete,
			deletedEventCount,
			run.EventCount,
		)
	}
	completed := remaining == 0 && deletedEventCount == run.EventCount
	if completed {
		if _, err := tx.ExecContext(ctx, `update usage_archive_runs set
			status = ?, completed_at_ms = ?, updated_at_ms = ?, last_error = null
		where id = ?`, StatusCompleted, nowMS, nowMS, runID); err != nil {
			return DeleteBatchResult{}, err
		}
		if err := releaseLock(ctx, tx, runID); err != nil {
			return DeleteBatchResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return DeleteBatchResult{}, err
	}
	updated, err := r.Run(ctx, runID)
	if err != nil {
		return DeleteBatchResult{}, err
	}
	return DeleteBatchResult{Deleted: len(candidates), LastID: lastID, Completed: completed, Run: updated}, nil
}

func (r *Repository) RecordFailure(ctx context.Context, runID, resumeStatus string, failure error, nowMS int64) (Run, error) {
	if failure == nil {
		return r.Run(ctx, runID)
	}
	if nowMS <= 0 {
		return Run{}, fmt.Errorf("nowMS must be greater than zero")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer func() { _ = tx.Rollback() }()
	run, err := runQuery(ctx, tx, runID)
	if err != nil {
		return Run{}, err
	}
	if run.Status != resumeStatus && !(run.Status == StatusFailed && run.ResumeStatus == resumeStatus) {
		// A stage may have committed successfully just before the caller
		// observed a context/readback error. Never downgrade that terminal or
		// different-stage state to failed as a side effect of recording the
		// stale error.
		return run, nil
	}
	if _, err := tx.ExecContext(ctx, `update usage_archive_runs set
		status = ?, resume_status = ?, last_error = ?, updated_at_ms = ?
	where id = ?`, StatusFailed, resumeStatus, failure.Error(), nowMS, runID); err != nil {
		return Run{}, err
	}
	if err := releaseLock(ctx, tx, runID); err != nil {
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return r.Run(ctx, runID)
}

func previewQuery(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, cutoffTimestampMS int64) (Preview, error) {
	var preview Preview
	preview.CutoffTimestampMS = cutoffTimestampMS
	if err := queryer.QueryRowContext(ctx, `select
		coalesce(max(e.id), 0),
		count(*),
		coalesce(sum(
			512 + length(coalesce(e.request_id, '')) + length(e.event_hash) + length(e.timestamp) +
			length(coalesce(e.provider, '')) + length(coalesce(e.executor_type, '')) + length(e.model) +
			length(coalesce(e.endpoint, '')) + length(coalesce(e.auth_index, '')) +
			length(coalesce(e.account_snapshot, '')) + length(coalesce(e.response_metadata_json, '')) +
			length(coalesce(e.fail_body, '')) + length(coalesce(e.raw_json, ''))
		), 0),
		coalesce(min(e.timestamp_ms), 0),
		coalesce(max(e.timestamp_ms), 0)
	from usage_events e
	where e.timestamp_ms < ?
		and not exists (
			select 1 from usage_archive_event_refs archived
			where archived.event_hash = e.event_hash
		)`, cutoffTimestampMS).Scan(
		&preview.TargetEventID,
		&preview.EventCount,
		&preview.EstimatedBytes,
		&preview.MinTimestampMS,
		&preview.MaxTimestampMS,
	); err != nil {
		return Preview{}, err
	}
	return preview, nil
}

func runQuery(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (Run, error) {
	run, err := scanRun(queryer.QueryRowContext(ctx, `select
		id, mode, schema_version, format, status, resume_status, requested_stage, cutoff_timestamp_ms,
		target_event_id, event_count, estimated_bytes, last_archived_event_id,
		archived_event_count, archived_uncompressed_bytes, archived_compressed_bytes,
		archive_digest, manifest_file, manifest_sha256, last_deleted_event_id,
		deleted_event_count, created_at_ms, updated_at_ms, started_at_ms, archived_at_ms,
		verified_at_ms, delete_started_at_ms, completed_at_ms, last_error
	from usage_archive_runs where id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, err
	}
	return run, nil
}

func scanRun(scanner interface{ Scan(...any) error }) (Run, error) {
	var run Run
	var resumeStatus, requestedStage, archiveDigest, manifestFile, manifestSHA256, lastError sql.NullString
	var startedAt, archivedAt, verifiedAt, deleteStartedAt, completedAt sql.NullInt64
	if err := scanner.Scan(
		&run.ID,
		&run.Mode,
		&run.SchemaVersion,
		&run.Format,
		&run.Status,
		&resumeStatus,
		&requestedStage,
		&run.CutoffTimestampMS,
		&run.TargetEventID,
		&run.EventCount,
		&run.EstimatedBytes,
		&run.LastArchivedEventID,
		&run.ArchivedEventCount,
		&run.ArchivedUncompressedBytes,
		&run.ArchivedCompressedBytes,
		&archiveDigest,
		&manifestFile,
		&manifestSHA256,
		&run.LastDeletedEventID,
		&run.DeletedEventCount,
		&run.CreatedAtMS,
		&run.UpdatedAtMS,
		&startedAt,
		&archivedAt,
		&verifiedAt,
		&deleteStartedAt,
		&completedAt,
		&lastError,
	); err != nil {
		return Run{}, err
	}
	run.ResumeStatus = resumeStatus.String
	run.RequestedStage = requestedStage.String
	run.ArchiveDigest = archiveDigest.String
	run.ManifestFile = manifestFile.String
	run.ManifestSHA256 = manifestSHA256.String
	run.LastError = lastError.String
	run.StartedAtMS = startedAt.Int64
	run.ArchivedAtMS = archivedAt.Int64
	run.VerifiedAtMS = verifiedAt.Int64
	run.DeleteStartedAtMS = deleteStartedAt.Int64
	run.CompletedAtMS = completedAt.Int64
	return run, nil
}

func scanSegment(scanner interface{ Scan(...any) error }) (Segment, error) {
	var segment Segment
	var verifiedAt sql.NullInt64
	if err := scanner.Scan(
		&segment.RunID,
		&segment.Sequence,
		&segment.Status,
		&segment.FileName,
		&segment.FirstEventID,
		&segment.LastEventID,
		&segment.MinTimestampMS,
		&segment.MaxTimestampMS,
		&segment.EventCount,
		&segment.UncompressedBytes,
		&segment.CompressedBytes,
		&segment.ContentSHA256,
		&segment.EventHashDigest,
		&segment.CreatedAtMS,
		&verifiedAt,
	); err != nil {
		return Segment{}, err
	}
	segment.VerifiedAtMS = verifiedAt.Int64
	return segment, nil
}

func acquireLock(ctx context.Context, tx *sql.Tx, runID, operation string, nowMS int64) error {
	var existingRunID string
	err := tx.QueryRowContext(ctx, `select run_id from usage_maintenance_locks where name = ?`, MaintenanceLockName).Scan(&existingRunID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `insert into usage_maintenance_locks (
			name, run_id, operation, acquired_at_ms, updated_at_ms
		) values (?, ?, ?, ?, ?)`, MaintenanceLockName, runID, operation, nowMS, nowMS)
		return err
	case err != nil:
		return err
	case existingRunID != runID:
		return fmt.Errorf("%w: run %s", ErrMaintenanceLocked, existingRunID)
	default:
		_, err = tx.ExecContext(ctx, `update usage_maintenance_locks set
			operation = ?, updated_at_ms = ? where name = ? and run_id = ?`, operation, nowMS, MaintenanceLockName, runID)
		return err
	}
}

func releaseLock(ctx context.Context, tx *sql.Tx, runID string) error {
	_, err := tx.ExecContext(ctx, `delete from usage_maintenance_locks where name = ? and run_id = ?`, MaintenanceLockName, runID)
	return err
}

func validateCoverage(ctx context.Context, tx *sql.Tx, run Run) error {
	if run.ArchivedEventCount != run.EventCount {
		return fmt.Errorf("%w: archived %d of %d events", ErrCoverageIncomplete, run.ArchivedEventCount, run.EventCount)
	}
	aggregateState, err := loadHourlyAggregateCoverageState(ctx, tx)
	if err != nil {
		return err
	}
	if err := validateHourlyAggregateCoverage(run, aggregateState); err != nil {
		return err
	}
	var archivedRefCount, deletedRefCount, identityCoverageCount int64
	if err := tx.QueryRowContext(ctx, `select
		count(*),
		coalesce(sum(case when archived.raw_deleted_at_ms is not null then 1 else 0 end), 0),
		coalesce(sum(case
			when ledger.aggregate_schema_version = ?
				and ledger.aggregate_structure_revision = ?
				and (
					(archived.raw_deleted_at_ms is null and ledger.raw_event_id = archived.raw_event_id)
					or (archived.raw_deleted_at_ms is not null and ledger.raw_event_id is null)
				)
			then 1 else 0 end), 0)
		from usage_archive_event_refs archived
		left join usage_event_identity_ledger ledger on ledger.event_hash = archived.event_hash
		where archived.run_id = ?`,
		aggregateState.SchemaVersion,
		aggregateState.StructureRevision,
		run.ID,
	).Scan(&archivedRefCount, &deletedRefCount, &identityCoverageCount); err != nil {
		return err
	}
	if archivedRefCount != run.EventCount || identityCoverageCount != run.EventCount {
		return fmt.Errorf(
			"%w: archive references=%d identity coverage=%d target=%d",
			ErrCoverageIncomplete,
			archivedRefCount,
			identityCoverageCount,
			run.EventCount,
		)
	}
	if deletedRefCount != run.DeletedEventCount {
		return fmt.Errorf(
			"%w: deleted references=%d recorded deletes=%d",
			ErrCoverageIncomplete,
			deletedRefCount,
			run.DeletedEventCount,
		)
	}
	return nil
}

func validateDeleteCoverage(ctx context.Context, tx *sql.Tx, run Run) error {
	if err := validateCoverage(ctx, tx, run); err != nil {
		return err
	}
	_, err := validateCurrentDeleteReadiness(ctx, tx, run)
	return err
}

type hourlyAggregateCoverageState struct {
	SchemaVersion     int
	StructureRevision string
	Status            string
	CoverageEventID   int64
}

func loadHourlyAggregateCoverageState(ctx context.Context, tx *sql.Tx) (hourlyAggregateCoverageState, error) {
	var state hourlyAggregateCoverageState
	err := tx.QueryRowContext(ctx, `select
		schema_version, structure_revision, status, coverage_event_id
		from usage_hourly_aggregate_state
		where aggregate_name = ?`, usageaggregate.AggregateName).Scan(
		&state.SchemaVersion,
		&state.StructureRevision,
		&state.Status,
		&state.CoverageEventID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return hourlyAggregateCoverageState{}, fmt.Errorf("%w: hourly aggregate state is missing", ErrCoverageIncomplete)
	}
	if err != nil {
		return hourlyAggregateCoverageState{}, err
	}
	return state, nil
}

func validateHourlyAggregateCoverage(run Run, state hourlyAggregateCoverageState) error {
	if state.SchemaVersion != usageaggregate.SchemaVersion ||
		!usageaggregate.IsCurrentStructureRevision(state.StructureRevision) ||
		state.Status != derivedStatusReady ||
		state.CoverageEventID < run.TargetEventID {
		return fmt.Errorf(
			"%w: hourly aggregate coverage=%d target=%d schema=%d revision=%q status=%s",
			ErrCoverageIncomplete,
			state.CoverageEventID,
			run.TargetEventID,
			state.SchemaVersion,
			state.StructureRevision,
			state.Status,
		)
	}
	return nil
}

func validateCurrentDeleteReadiness(
	ctx context.Context,
	tx *sql.Tx,
	run Run,
) (hourlyAggregateCoverageState, error) {
	aggregateState, err := loadHourlyAggregateCoverageState(ctx, tx)
	if err != nil {
		return hourlyAggregateCoverageState{}, err
	}
	if err := validateHourlyAggregateCoverage(run, aggregateState); err != nil {
		return hourlyAggregateCoverageState{}, err
	}

	var migrationStatus string
	err = tx.QueryRowContext(ctx, `select status from usage_data_migrations where name = ?`,
		datamigration.UsageCacheAccountingMigrationName,
	).Scan(&migrationStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return hourlyAggregateCoverageState{}, fmt.Errorf("%w: accounting migration state is missing", ErrCoverageIncomplete)
	}
	if err != nil {
		return hourlyAggregateCoverageState{}, err
	}
	if migrationStatus != datamigration.StatusCompleted {
		return hourlyAggregateCoverageState{}, fmt.Errorf(
			"%w: accounting migration status=%s",
			ErrCoverageIncomplete,
			migrationStatus,
		)
	}

	pricingRevision, err := usagepricing.StructureRevision(ctx, tx)
	if err != nil {
		return hourlyAggregateCoverageState{}, err
	}
	var pricingVersion int
	var pricingStateRevision, pricingStatus string
	var pricingCoverage int64
	err = tx.QueryRowContext(ctx, `select
		schema_version, structure_revision, status, coverage_event_id
		from usage_pricing_rollup_state where rollup_name = ?`,
		usagepricing.RollupName,
	).Scan(&pricingVersion, &pricingStateRevision, &pricingStatus, &pricingCoverage)
	if errors.Is(err, sql.ErrNoRows) {
		return hourlyAggregateCoverageState{}, fmt.Errorf("%w: pricing rollup state is missing", ErrCoverageIncomplete)
	}
	if err != nil {
		return hourlyAggregateCoverageState{}, err
	}
	if err := validateDerivedCoverage(
		"pricing",
		pricingVersion,
		usagepricing.SchemaVersion,
		pricingStateRevision,
		pricingRevision,
		pricingStatus,
		pricingCoverage,
		run.TargetEventID,
	); err != nil {
		return hourlyAggregateCoverageState{}, err
	}

	for _, stateRequirement := range []struct {
		name             string
		expectedRevision string
	}{
		{name: usagemonitoring.StatsRollupName, expectedRevision: pricingRevision},
		{name: usagemonitoring.MetadataRollupName, expectedRevision: usageidentity.ModelFormatVersion},
		{name: usagemonitoring.ProjectionRollupName, expectedRevision: usageidentity.ModelFormatVersion},
	} {
		var version int
		var revision, status string
		var coverage int64
		err := tx.QueryRowContext(ctx, `select
			schema_version, structure_revision, status, coverage_event_id
			from usage_monitoring_rollup_state where rollup_name = ?`,
			stateRequirement.name,
		).Scan(&version, &revision, &status, &coverage)
		if errors.Is(err, sql.ErrNoRows) {
			return hourlyAggregateCoverageState{}, fmt.Errorf(
				"%w: monitoring %s state is missing",
				ErrCoverageIncomplete,
				stateRequirement.name,
			)
		}
		if err != nil {
			return hourlyAggregateCoverageState{}, err
		}
		if err := validateDerivedCoverage(
			"monitoring "+stateRequirement.name,
			version,
			usagemonitoring.SchemaVersion,
			revision,
			stateRequirement.expectedRevision,
			status,
			coverage,
			run.TargetEventID,
		); err != nil {
			return hourlyAggregateCoverageState{}, err
		}
	}

	var searchReady int
	if err := tx.QueryRowContext(ctx, `select ready
		from usage_monitoring_search_index_state where id = 1`).Scan(&searchReady); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return hourlyAggregateCoverageState{}, fmt.Errorf("%w: monitoring search state is missing", ErrCoverageIncomplete)
		}
		return hourlyAggregateCoverageState{}, err
	}
	if searchReady != 1 {
		return hourlyAggregateCoverageState{}, fmt.Errorf("%w: monitoring search index is not ready", ErrCoverageIncomplete)
	}

	for _, checkpoint := range []string{
		usagerollup.AccountHistoryCheckpointName,
		usagerollup.DashboardHourlyCheckpointName,
	} {
		var coverage int64
		if err := tx.QueryRowContext(ctx, `select last_event_id
			from usage_rollup_checkpoints where name = ?`, checkpoint).Scan(&coverage); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return hourlyAggregateCoverageState{}, fmt.Errorf(
					"%w: %s checkpoint is missing",
					ErrCoverageIncomplete,
					checkpoint,
				)
			}
			return hourlyAggregateCoverageState{}, err
		}
		if coverage < run.TargetEventID {
			return hourlyAggregateCoverageState{}, fmt.Errorf(
				"%w: %s coverage=%d target=%d",
				ErrCoverageIncomplete,
				checkpoint,
				coverage,
				run.TargetEventID,
			)
		}
	}
	return aggregateState, nil
}

func validateDerivedCoverage(
	name string,
	actualVersion int,
	expectedVersion int,
	actualRevision string,
	expectedRevision string,
	status string,
	coverageEventID int64,
	targetEventID int64,
) error {
	if actualVersion != expectedVersion ||
		strings.TrimSpace(actualRevision) == "" ||
		actualRevision != expectedRevision ||
		status != derivedStatusReady ||
		coverageEventID < targetEventID {
		return fmt.Errorf(
			"%w: %s coverage=%d target=%d schema=%d expected_schema=%d revision=%q expected_revision=%q status=%s",
			ErrCoverageIncomplete,
			name,
			coverageEventID,
			targetEventID,
			actualVersion,
			expectedVersion,
			actualRevision,
			expectedRevision,
			status,
		)
	}
	return nil
}

func containsStatus(statuses []string, candidate string) bool {
	for _, status := range statuses {
		if status == candidate {
			return true
		}
	}
	return false
}

func validateRunContract(run Run) error {
	if run.SchemaVersion == SchemaVersion && run.Format == FormatGzipJSONLV1 {
		return nil
	}
	return fmt.Errorf(
		"%w: unsupported archive contract schema=%d format=%q",
		ErrInvalidState,
		run.SchemaVersion,
		run.Format,
	)
}

const archiveRecordExpression = `json_patch(
	json_patch(
		json_object(
				'_cpamp_archive_schema_version', ?,
			'_cpamp_archive_event_id', e.id,
			'request_id', coalesce(e.request_id, ''),
			'event_hash', e.event_hash,
			'timestamp_ms', e.timestamp_ms,
			'timestamp', e.timestamp,
			'provider', coalesce(e.provider, ''),
			'executor_type', coalesce(e.executor_type, ''),
			'model', e.model,
			'endpoint', coalesce(e.endpoint, ''),
			'method', coalesce(e.method, ''),
			'path', coalesce(e.path, ''),
			'client_ip', coalesce(e.client_ip, ''),
			'x_forwarded_for', coalesce(e.x_forwarded_for, ''),
			'user_agent', coalesce(e.user_agent, ''),
			'auth_type', coalesce(e.auth_type, ''),
			'auth_index', coalesce(e.auth_index, ''),
			'source', coalesce(e.source, ''),
			'source_hash', coalesce(e.source_hash, ''),
			'api_key_hash', coalesce(e.api_key_hash, ''),
			'account_snapshot', coalesce(e.account_snapshot, ''),
			'auth_label_snapshot', coalesce(e.auth_label_snapshot, ''),
			'auth_file_snapshot', coalesce(e.auth_file_snapshot, '')
		),
		json_object(
			'auth_provider_snapshot', coalesce(e.auth_provider_snapshot, ''),
			'auth_project_id_snapshot', coalesce(e.auth_project_id_snapshot, ''),
			'auth_snapshot_at_ms', coalesce(e.auth_snapshot_at_ms, 0),
			'requested_model', coalesce(e.requested_model, ''),
			'resolved_model', coalesce(e.resolved_model, ''),
			'reasoning_effort', coalesce(e.reasoning_effort, ''),
			'service_tier', coalesce(e.service_tier, ''),
			'request_service_tier', coalesce(e.request_service_tier, ''),
			'response_service_tier', coalesce(e.response_service_tier, ''),
			'cache_input_mode', coalesce(e.cache_input_mode, ''),
			'input_tokens', e.input_tokens,
			'output_tokens', e.output_tokens,
			'reasoning_tokens', e.reasoning_tokens,
			'cached_tokens', e.cached_tokens,
			'cache_tokens', e.cache_tokens,
			'cache_read_tokens', e.cache_read_tokens,
			'cache_creation_tokens', e.cache_creation_tokens,
			'normalized_uncached_input_tokens', coalesce(e.normalized_uncached_input_tokens, 0),
			'normalized_total_input_tokens', coalesce(e.normalized_total_input_tokens, 0),
			'normalized_cache_read_tokens', coalesce(e.normalized_cache_read_tokens, 0)
		)
	),
	json_object(
		'normalized_cache_creation_tokens', coalesce(e.normalized_cache_creation_tokens, 0),
		'total_tokens', e.total_tokens,
		'latency_ms', e.latency_ms,
		'ttft_ms', e.ttft_ms,
		'failed', e.failed,
		'fail_status_code', coalesce(e.fail_status_code, 0),
		'fail_summary', coalesce(e.fail_summary, ''),
			'response_metadata', case
				when json_valid(e.response_metadata_json) then json(e.response_metadata_json)
				else null
			end,
			'response_metadata_json', case
				when json_valid(e.response_metadata_json) then e.response_metadata_json
				else ''
			end,
			'header_quota_recover_at_ms', coalesce(e.header_quota_recover_at_ms, 0),
		'header_quota_used_percent', e.header_quota_used_percent,
		'header_quota_plan_type', coalesce(e.header_quota_plan_type, ''),
		'header_error_kind', coalesce(e.header_error_kind, ''),
		'header_error_code', coalesce(e.header_error_code, ''),
		'header_trace_id', coalesce(e.header_trace_id, ''),
		'fail_body', coalesce(e.fail_body, ''),
		'raw_json', coalesce(e.raw_json, ''),
		'created_at_ms', e.created_at_ms
	)
)`
