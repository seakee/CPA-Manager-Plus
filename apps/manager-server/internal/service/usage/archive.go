package usage

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/datamigration"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageaggregate"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagearchive"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	usageparser "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const (
	defaultArchiveSegmentEventLimit = 10_000
	defaultArchiveSegmentByteLimit  = 64 * 1024 * 1024
	defaultArchiveDeleteBatchSize   = 1_000
	archiveReadinessBackfillBatch   = 1_000
	maxArchiveRecordBytes           = usageparser.MaxJSONLRecordBytes
	maxArchiveSegmentBytes          = 64 * 1024 * 1024
	maxArchiveManifestBytes         = 4 * 1024 * 1024
	maxArchiveListLimit             = 100
)

var (
	ErrArchiveUnavailable        = errors.New("usage archive is not configured")
	ErrArchiveInvalidRequest     = errors.New("invalid usage archive request")
	ErrArchiveDeleteUnavailable  = errors.New("usage archive delete requires permanent hourly aggregate reads")
	ErrArchiveInvalidID          = errors.New("invalid usage archive run id")
	ErrArchiveNotFound           = usagearchive.ErrNotFound
	ErrArchiveNoEvents           = usagearchive.ErrNoEvents
	ErrArchiveInvalidState       = usagearchive.ErrInvalidState
	ErrArchiveMaintenanceLocked  = usagearchive.ErrMaintenanceLocked
	ErrArchiveCoverageIncomplete = usagearchive.ErrCoverageIncomplete
)

type ArchiveConfig struct {
	Directory             string
	SegmentEventLimit     int
	SegmentByteLimit      int64
	DeleteBatchSize       int
	AggregateReadsEnabled bool
}

type ArchiveStatus struct {
	Run      store.UsageArchiveRun       `json:"run"`
	Segments []store.UsageArchiveSegment `json:"segments"`
}

// ArchiveRunSummary is the public archive progress view. It intentionally
// excludes archive paths, digests, formats, schemas, and raw internal errors.
type ArchiveRunSummary struct {
	ID                        string `json:"id"`
	Mode                      string `json:"mode"`
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
	LastDeletedEventID        int64  `json:"last_deleted_event_id"`
	DeletedEventCount         int64  `json:"deleted_event_count"`
	CreatedAtMS               int64  `json:"created_at_ms"`
	UpdatedAtMS               int64  `json:"updated_at_ms"`
	StartedAtMS               int64  `json:"started_at_ms,omitempty"`
	ArchivedAtMS              int64  `json:"archived_at_ms,omitempty"`
	VerifiedAtMS              int64  `json:"verified_at_ms,omitempty"`
	DeleteStartedAtMS         int64  `json:"delete_started_at_ms,omitempty"`
	CompletedAtMS             int64  `json:"completed_at_ms,omitempty"`
	HasError                  bool   `json:"has_error"`
}

// ArchiveSegmentSummary exposes progress and size metadata without publishing
// segment file names or integrity digests.
type ArchiveSegmentSummary struct {
	RunID             string `json:"run_id"`
	Sequence          int    `json:"sequence"`
	Status            string `json:"status"`
	FirstEventID      int64  `json:"first_event_id"`
	LastEventID       int64  `json:"last_event_id"`
	MinTimestampMS    int64  `json:"min_timestamp_ms"`
	MaxTimestampMS    int64  `json:"max_timestamp_ms"`
	EventCount        int64  `json:"event_count"`
	UncompressedBytes int64  `json:"uncompressed_bytes"`
	CompressedBytes   int64  `json:"compressed_bytes"`
	CreatedAtMS       int64  `json:"created_at_ms"`
	VerifiedAtMS      int64  `json:"verified_at_ms,omitempty"`
}

type ArchiveStatusSummary struct {
	Run      ArchiveRunSummary       `json:"run"`
	Segments []ArchiveSegmentSummary `json:"segments"`
}

type ArchiveList struct {
	Runs         []ArchiveRunSummary `json:"runs"`
	Total        int64               `json:"total"`
	StatusCounts map[string]int64    `json:"status_counts"`
	NextCursor   string              `json:"next_cursor,omitempty"`
}

type ArchiveListOptions struct {
	Status string
	Mode   string
	Limit  int
	Cursor string
}

type MaintenanceMigrationSummary struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	LastEventID   int64  `json:"last_event_id"`
	TargetEventID int64  `json:"target_event_id"`
	ProcessedRows int64  `json:"processed_rows"`
	ChangedRows   int64  `json:"changed_rows"`
	UpdatedAtMS   int64  `json:"updated_at_ms"`
}

type MaintenanceAggregateSummary struct {
	Name            string `json:"name"`
	SchemaVersion   int    `json:"schema_version"`
	Status          string `json:"status"`
	CoverageEventID int64  `json:"coverage_event_id"`
	TargetEventID   int64  `json:"target_event_id"`
	UpdatedAtMS     int64  `json:"updated_at_ms"`
}

type MaintenanceLockSummary struct {
	RunID        string `json:"run_id"`
	Operation    string `json:"operation"`
	AcquiredAtMS int64  `json:"acquired_at_ms"`
	UpdatedAtMS  int64  `json:"updated_at_ms"`
}

type MaintenanceReadiness struct {
	MigrationReady       bool `json:"migration_ready"`
	HourlyAggregateReady bool `json:"hourly_aggregate_ready"`
	ArchiveDeleteEnabled bool `json:"archive_delete_enabled"`
}

type MaintenanceCoverageSummary struct {
	Status           string `json:"status"`
	WatermarkEventID int64  `json:"watermark_event_id"`
	TargetEventID    int64  `json:"target_event_id"`
	Complete         bool   `json:"complete"`
}

type MaintenanceStatus struct {
	RawEventCount                int64                       `json:"raw_event_count"`
	RawMinTimestampMS            int64                       `json:"raw_min_timestamp_ms"`
	RawMaxTimestampMS            int64                       `json:"raw_max_timestamp_ms"`
	RawArchivedEventCount        int64                       `json:"raw_archived_event_count"`
	RawDeletedEventCount         int64                       `json:"raw_deleted_event_count"`
	ActiveRun                    *ArchiveRunSummary          `json:"active_run,omitempty"`
	ActiveLock                   *MaintenanceLockSummary     `json:"active_lock,omitempty"`
	Migration                    MaintenanceMigrationSummary `json:"migration"`
	HourlyAggregate              MaintenanceAggregateSummary `json:"hourly_aggregate"`
	Readiness                    MaintenanceReadiness        `json:"readiness"`
	MigrationCoverage            MaintenanceCoverageSummary  `json:"migration_coverage"`
	HourlyAggregateCoverage      MaintenanceCoverageSummary  `json:"hourly_aggregate_coverage"`
	Storage                      store.SQLitePageStats       `json:"storage"`
	CompactRequiresStoppedServer bool                        `json:"compact_requires_stopped_server"`
}

type ArchiveManifest struct {
	SchemaVersion     int                      `json:"schema_version"`
	Format            string                   `json:"format"`
	RunID             string                   `json:"run_id"`
	Mode              string                   `json:"mode"`
	CutoffTimestampMS int64                    `json:"cutoff_timestamp_ms"`
	FirstEventID      int64                    `json:"first_event_id"`
	LastEventID       int64                    `json:"last_event_id"`
	MinTimestampMS    int64                    `json:"min_timestamp_ms"`
	MaxTimestampMS    int64                    `json:"max_timestamp_ms"`
	TargetEventID     int64                    `json:"target_event_id"`
	EventCount        int64                    `json:"event_count"`
	UncompressedBytes int64                    `json:"uncompressed_bytes"`
	CompressedBytes   int64                    `json:"compressed_bytes"`
	EventHashDigest   string                   `json:"event_hash_digest"`
	GeneratedAtMS     int64                    `json:"generated_at_ms"`
	Segments          []ArchiveManifestSegment `json:"segments"`
}

type ArchiveManifestSegment struct {
	Sequence          int    `json:"sequence"`
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
}

type archiveManager struct {
	store     *store.Store
	config    ArchiveConfig
	configErr error
	mu        sync.Mutex
	testHook  func(string) error
}

type archiveRecordEnvelope struct {
	SchemaVersion int    `json:"_cpamp_archive_schema_version"`
	EventID       int64  `json:"_cpamp_archive_event_id"`
	EventHash     string `json:"event_hash"`
	TimestampMS   int64  `json:"timestamp_ms"`
}

type archiveFileInspection struct {
	EventCount        int64
	FirstEventID      int64
	LastEventID       int64
	MinTimestampMS    int64
	MaxTimestampMS    int64
	UncompressedBytes int64
	CompressedBytes   int64
	ContentSHA256     string
	EventHashDigest   string
}

func WithArchive(config ArchiveConfig) Option {
	return func(service *Service) {
		service.archive = newArchiveManager(service.store, config)
		service.archiveJobs = newArchiveJobRunner(service)
	}
}

func newArchiveManager(st *store.Store, config ArchiveConfig) *archiveManager {
	if config.SegmentEventLimit <= 0 {
		config.SegmentEventLimit = defaultArchiveSegmentEventLimit
	}
	if config.SegmentByteLimit <= 0 || config.SegmentByteLimit > maxArchiveSegmentBytes {
		config.SegmentByteLimit = defaultArchiveSegmentByteLimit
	}
	if config.DeleteBatchSize <= 0 {
		config.DeleteBatchSize = defaultArchiveDeleteBatchSize
	}
	directory := strings.TrimSpace(config.Directory)
	manager := &archiveManager{store: st, config: config}
	if directory == "" {
		manager.configErr = ErrArchiveUnavailable
		return manager
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		manager.configErr = fmt.Errorf("resolve usage archive directory: %w", err)
		return manager
	}
	resolved, err := resolveArchiveDirectory(filepath.Clean(absolute))
	if err != nil {
		manager.configErr = fmt.Errorf("resolve usage archive directory parent: %w", err)
		return manager
	}
	manager.config.Directory = resolved
	return manager
}

func (s *Service) PreviewArchive(ctx context.Context, cutoffTimestampMS int64) (store.UsageArchivePreview, error) {
	if err := validateArchiveCutoff(cutoffTimestampMS); err != nil {
		return store.UsageArchivePreview{}, err
	}
	manager, err := s.requireArchiveManager()
	if err != nil {
		return store.UsageArchivePreview{}, err
	}
	return manager.store.UsageArchives.Preview(ctx, cutoffTimestampMS)
}

func (s *Service) CreateArchive(ctx context.Context, cutoffTimestampMS int64) (ArchiveStatus, error) {
	if err := validateArchiveCutoff(cutoffTimestampMS); err != nil {
		return ArchiveStatus{}, err
	}
	manager, err := s.requireArchiveManager()
	if err != nil {
		return ArchiveStatus{}, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	runID, err := newArchiveRunID()
	if err != nil {
		return ArchiveStatus{}, err
	}
	run, err := manager.store.UsageArchives.CreateRun(ctx, runID, cutoffTimestampMS, time.Now().UnixMilli())
	if err != nil {
		return ArchiveStatus{}, err
	}
	return ArchiveStatus{Run: run, Segments: []store.UsageArchiveSegment{}}, nil
}

func (s *Service) CreateRetentionArchive(ctx context.Context, cutoffTimestampMS int64) (ArchiveStatus, error) {
	if err := validateArchiveCutoff(cutoffTimestampMS); err != nil {
		return ArchiveStatus{}, err
	}
	manager, err := s.requireArchiveManager()
	if err != nil {
		return ArchiveStatus{}, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	runID, err := newArchiveRunID()
	if err != nil {
		return ArchiveStatus{}, err
	}
	run, err := manager.store.UsageArchives.CreateRetentionRun(ctx, runID, cutoffTimestampMS, time.Now().UnixMilli())
	if err != nil {
		return ArchiveStatus{}, err
	}
	return ArchiveStatus{Run: run, Segments: []store.UsageArchiveSegment{}}, nil
}

func (s *Service) ArchiveStatus(ctx context.Context, runID string) (ArchiveStatus, error) {
	manager, err := s.requireArchiveManager()
	if err != nil {
		return ArchiveStatus{}, err
	}
	return manager.status(ctx, runID)
}

func (s *Service) ListArchives(ctx context.Context, limit int) (ArchiveList, error) {
	return s.ListArchivePage(ctx, ArchiveListOptions{Limit: limit})
}

func (s *Service) ListArchivePage(ctx context.Context, options ArchiveListOptions) (ArchiveList, error) {
	manager, err := s.requireArchiveManager()
	if err != nil {
		return ArchiveList{}, err
	}
	if options.Limit <= 0 {
		options.Limit = 20
	}
	if options.Limit > maxArchiveListLimit {
		return ArchiveList{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrArchiveInvalidRequest, maxArchiveListLimit)
	}
	if !validArchiveStatusFilter(options.Status) {
		return ArchiveList{}, fmt.Errorf("%w: unsupported archive status filter", ErrArchiveInvalidRequest)
	}
	if !validArchiveModeFilter(options.Mode) {
		return ArchiveList{}, fmt.Errorf("%w: unsupported archive mode filter", ErrArchiveInvalidRequest)
	}
	createdAtMS, id, err := decodeArchiveListCursor(options.Cursor)
	if err != nil {
		return ArchiveList{}, err
	}
	result, err := manager.store.ListUsageArchiveRuns(ctx, store.UsageArchiveRunListFilter{
		Status:            options.Status,
		Mode:              options.Mode,
		Limit:             options.Limit,
		BeforeCreatedAtMS: createdAtMS,
		BeforeID:          id,
	})
	if err != nil {
		return ArchiveList{}, err
	}
	summaries := make([]ArchiveRunSummary, 0, len(result.Runs))
	for _, run := range result.Runs {
		summaries = append(summaries, summarizeArchiveRun(run))
	}
	nextCursor := ""
	if result.HasMore && len(result.Runs) > 0 {
		last := result.Runs[len(result.Runs)-1]
		nextCursor = encodeArchiveListCursor(last.CreatedAtMS, last.ID)
	}
	return ArchiveList{
		Runs:         summaries,
		Total:        result.Total,
		StatusCounts: result.StatusCounts,
		NextCursor:   nextCursor,
	}, nil
}

func validArchiveStatusFilter(status string) bool {
	switch strings.TrimSpace(status) {
	case "", usagearchive.StatusPreviewed, usagearchive.StatusArchiving, usagearchive.StatusArchived,
		usagearchive.StatusVerifying, usagearchive.StatusVerified, usagearchive.StatusDeleting,
		usagearchive.StatusCompleted, usagearchive.StatusFailed, usagearchive.StatusCancelled:
		return true
	default:
		return false
	}
}

func validArchiveModeFilter(mode string) bool {
	switch strings.TrimSpace(mode) {
	case "", usagearchive.RunModeManual, usagearchive.RunModeRetention:
		return true
	default:
		return false
	}
}

func encodeArchiveListCursor(createdAtMS int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(createdAtMS, 10) + ":" + id))
}

func decodeArchiveListCursor(cursor string) (int64, string, error) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return 0, "", nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, "", fmt.Errorf("%w: invalid archive cursor", ErrArchiveInvalidRequest)
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 || !validArchiveRunID(parts[1]) {
		return 0, "", fmt.Errorf("%w: invalid archive cursor", ErrArchiveInvalidRequest)
	}
	createdAtMS, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || createdAtMS <= 0 {
		return 0, "", fmt.Errorf("%w: invalid archive cursor", ErrArchiveInvalidRequest)
	}
	return createdAtMS, parts[1], nil
}

func (s *Service) MaintenanceStatus(ctx context.Context) (MaintenanceStatus, error) {
	manager, err := s.requireArchiveManager()
	if err != nil {
		return MaintenanceStatus{}, err
	}
	counts, err := manager.store.UsageMaintenanceCounts(ctx)
	if err != nil {
		return MaintenanceStatus{}, err
	}
	activeRun, hasActiveRun, err := manager.store.ActiveUsageArchiveRun(ctx)
	if err != nil {
		return MaintenanceStatus{}, err
	}
	lock, hasLock, err := manager.store.UsageMaintenanceLock(ctx)
	if err != nil {
		return MaintenanceStatus{}, err
	}
	migration, err := manager.store.UsageCacheAccountingMigrationState(ctx)
	if err != nil {
		return MaintenanceStatus{}, err
	}
	aggregate, err := manager.store.UsageHourlyAggregateState(ctx)
	if err != nil {
		return MaintenanceStatus{}, err
	}
	latestEventID, err := manager.store.LatestUsageEventID(ctx)
	if err != nil {
		return MaintenanceStatus{}, err
	}
	pageStats, err := manager.store.SQLitePageStats(ctx)
	if err != nil {
		return MaintenanceStatus{}, err
	}

	aggregateTargetEventID := max(aggregate.TargetEventID, latestEventID)
	status := MaintenanceStatus{
		RawEventCount:         counts.RawEventCount,
		RawMinTimestampMS:     counts.RawMinTimestampMS,
		RawMaxTimestampMS:     counts.RawMaxTimestampMS,
		RawArchivedEventCount: counts.RawArchivedEventCount,
		RawDeletedEventCount:  counts.RawDeletedEventCount,
		Migration: MaintenanceMigrationSummary{
			Name:          migration.Name,
			Status:        migration.Status,
			LastEventID:   migration.LastEventID,
			TargetEventID: migration.TargetEventID,
			ProcessedRows: migration.ProcessedRows,
			ChangedRows:   migration.ChangedRows,
			UpdatedAtMS:   migration.UpdatedAtMS,
		},
		HourlyAggregate: MaintenanceAggregateSummary{
			Name:            aggregate.AggregateName,
			SchemaVersion:   aggregate.SchemaVersion,
			Status:          aggregate.Status,
			CoverageEventID: aggregate.CoverageEventID,
			TargetEventID:   aggregateTargetEventID,
			UpdatedAtMS:     aggregate.UpdatedAtMS,
		},
		Readiness: MaintenanceReadiness{
			MigrationReady: migration.Status == datamigration.StatusCompleted,
			HourlyAggregateReady: aggregate.SchemaVersion == usageaggregate.SchemaVersion &&
				aggregate.Status == "ready" &&
				usageaggregate.IsCurrentStructureRevision(aggregate.StructureRevision) &&
				aggregate.CoverageEventID >= aggregateTargetEventID,
			ArchiveDeleteEnabled: manager.config.AggregateReadsEnabled,
		},
		MigrationCoverage: MaintenanceCoverageSummary{
			Status:           migration.Status,
			WatermarkEventID: migration.LastEventID,
			TargetEventID:    migration.TargetEventID,
			Complete:         migration.Status == datamigration.StatusCompleted,
		},
		HourlyAggregateCoverage: MaintenanceCoverageSummary{
			Status:           aggregate.Status,
			WatermarkEventID: aggregate.CoverageEventID,
			TargetEventID:    aggregateTargetEventID,
			Complete: aggregate.SchemaVersion == usageaggregate.SchemaVersion &&
				aggregate.Status == "ready" &&
				usageaggregate.IsCurrentStructureRevision(aggregate.StructureRevision) &&
				aggregate.CoverageEventID >= aggregateTargetEventID,
		},
		Storage:                      pageStats,
		CompactRequiresStoppedServer: true,
	}
	if hasActiveRun {
		summary := summarizeArchiveRun(activeRun)
		status.ActiveRun = &summary
	}
	if hasLock {
		status.ActiveLock = &MaintenanceLockSummary{
			RunID:        lock.RunID,
			Operation:    lock.Operation,
			AcquiredAtMS: lock.AcquiredAtMS,
			UpdatedAtMS:  lock.UpdatedAtMS,
		}
	}
	return status, nil
}

func (s *Service) ActiveArchiveRun(ctx context.Context) (store.UsageArchiveRun, bool, error) {
	manager, err := s.requireArchiveManager()
	if err != nil {
		return store.UsageArchiveRun{}, false, err
	}
	return manager.store.ActiveUsageArchiveRun(ctx)
}

func (s *Service) ResumeArchive(ctx context.Context, runID string) (ArchiveStatus, error) {
	manager, err := s.requireArchiveManager()
	if err != nil {
		return ArchiveStatus{}, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.resumeLocked(ctx, runID, "")
}

func (s *Service) ResumeArchiveAtStage(ctx context.Context, runID, expectedStage string) (ArchiveStatus, error) {
	if archiveResumeStageOrder(expectedStage) == 0 {
		return ArchiveStatus{}, fmt.Errorf("%w: invalid expected archive resume stage %q", ErrArchiveInvalidRequest, expectedStage)
	}
	manager, err := s.requireArchiveManager()
	if err != nil {
		return ArchiveStatus{}, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.resumeLocked(ctx, runID, expectedStage)
}

func (m *archiveManager) resumeLocked(ctx context.Context, runID, expectedStage string) (ArchiveStatus, error) {
	status, err := m.status(ctx, runID)
	if err != nil {
		return ArchiveStatus{}, err
	}
	if expectedStage != "" {
		currentStageOrder := archiveRunResumeStageOrder(status.Run)
		expectedStageOrder := archiveResumeStageOrder(expectedStage)
		if currentStageOrder > expectedStageOrder {
			return status, nil
		}
		if currentStageOrder == 0 || currentStageOrder < expectedStageOrder {
			return ArchiveStatus{}, fmt.Errorf(
				"%w: cannot resume expected %s stage from %s",
				ErrArchiveInvalidState,
				expectedStage,
				status.Run.Status,
			)
		}
	}
	switch status.Run.Status {
	case usagearchive.StatusPreviewed, usagearchive.StatusArchiving:
		return m.archiveLocked(ctx, status.Run.ID)
	case usagearchive.StatusVerifying:
		return m.verifyLocked(ctx, status.Run.ID)
	case usagearchive.StatusDeleting:
		return m.deleteLocked(ctx, status.Run.ID)
	case usagearchive.StatusFailed:
		switch status.Run.ResumeStatus {
		case usagearchive.StatusArchiving:
			return m.archiveLocked(ctx, status.Run.ID)
		case usagearchive.StatusVerifying:
			return m.verifyLocked(ctx, status.Run.ID)
		case usagearchive.StatusDeleting:
			return m.deleteLocked(ctx, status.Run.ID)
		default:
			return ArchiveStatus{}, fmt.Errorf("%w: failed run has invalid resume status %q", ErrArchiveInvalidState, status.Run.ResumeStatus)
		}
	case usagearchive.StatusArchived, usagearchive.StatusVerified, usagearchive.StatusCompleted:
		return status, nil
	default:
		return ArchiveStatus{}, fmt.Errorf("%w: cannot resume run in %s", ErrArchiveInvalidState, status.Run.Status)
	}
}

func archiveResumeStageOrder(stage string) int {
	switch stage {
	case usagearchive.StatusArchiving:
		return 1
	case usagearchive.StatusVerifying:
		return 2
	case usagearchive.StatusDeleting:
		return 3
	default:
		return 0
	}
}

func archiveRunResumeStageOrder(run store.UsageArchiveRun) int {
	switch run.Status {
	case usagearchive.StatusPreviewed, usagearchive.StatusArchiving:
		return archiveResumeStageOrder(usagearchive.StatusArchiving)
	case usagearchive.StatusArchived, usagearchive.StatusVerifying:
		return archiveResumeStageOrder(usagearchive.StatusVerifying)
	case usagearchive.StatusVerified, usagearchive.StatusDeleting:
		return archiveResumeStageOrder(usagearchive.StatusDeleting)
	case usagearchive.StatusCompleted:
		return 4
	case usagearchive.StatusFailed:
		return archiveResumeStageOrder(run.ResumeStatus)
	default:
		return 0
	}
}

func (s *Service) VerifyArchive(ctx context.Context, runID string) (ArchiveStatus, error) {
	manager, err := s.requireArchiveManager()
	if err != nil {
		return ArchiveStatus{}, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.verifyLocked(ctx, runID)
}

func (s *Service) DeleteArchive(ctx context.Context, runID string) (ArchiveStatus, error) {
	manager, err := s.requireArchiveManager()
	if err != nil {
		return ArchiveStatus{}, err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.deleteLocked(ctx, runID)
}

func (s *Service) requireArchiveManager() (*archiveManager, error) {
	if s.archive == nil {
		return nil, ErrArchiveUnavailable
	}
	if s.archive.configErr != nil {
		if errors.Is(s.archive.configErr, ErrArchiveUnavailable) {
			return nil, s.archive.configErr
		}
		return nil, fmt.Errorf("%w: %v", ErrArchiveUnavailable, s.archive.configErr)
	}
	return s.archive, nil
}

func validateArchiveCutoff(cutoffTimestampMS int64) error {
	if cutoffTimestampMS <= 0 {
		return fmt.Errorf("%w: cutoff_timestamp_ms must be greater than zero", ErrArchiveInvalidRequest)
	}
	return nil
}

func NewArchiveStatusSummary(status ArchiveStatus) ArchiveStatusSummary {
	segments := make([]ArchiveSegmentSummary, 0, len(status.Segments))
	for _, segment := range status.Segments {
		segments = append(segments, ArchiveSegmentSummary{
			RunID:             segment.RunID,
			Sequence:          segment.Sequence,
			Status:            segment.Status,
			FirstEventID:      segment.FirstEventID,
			LastEventID:       segment.LastEventID,
			MinTimestampMS:    segment.MinTimestampMS,
			MaxTimestampMS:    segment.MaxTimestampMS,
			EventCount:        segment.EventCount,
			UncompressedBytes: segment.UncompressedBytes,
			CompressedBytes:   segment.CompressedBytes,
			CreatedAtMS:       segment.CreatedAtMS,
			VerifiedAtMS:      segment.VerifiedAtMS,
		})
	}
	return ArchiveStatusSummary{
		Run:      summarizeArchiveRun(status.Run),
		Segments: segments,
	}
}

func summarizeArchiveRun(run store.UsageArchiveRun) ArchiveRunSummary {
	return ArchiveRunSummary{
		ID:                        run.ID,
		Mode:                      run.Mode,
		Status:                    run.Status,
		ResumeStatus:              run.ResumeStatus,
		RequestedStage:            run.RequestedStage,
		CutoffTimestampMS:         run.CutoffTimestampMS,
		TargetEventID:             run.TargetEventID,
		EventCount:                run.EventCount,
		EstimatedBytes:            run.EstimatedBytes,
		LastArchivedEventID:       run.LastArchivedEventID,
		ArchivedEventCount:        run.ArchivedEventCount,
		ArchivedUncompressedBytes: run.ArchivedUncompressedBytes,
		ArchivedCompressedBytes:   run.ArchivedCompressedBytes,
		LastDeletedEventID:        run.LastDeletedEventID,
		DeletedEventCount:         run.DeletedEventCount,
		CreatedAtMS:               run.CreatedAtMS,
		UpdatedAtMS:               run.UpdatedAtMS,
		StartedAtMS:               run.StartedAtMS,
		ArchivedAtMS:              run.ArchivedAtMS,
		VerifiedAtMS:              run.VerifiedAtMS,
		DeleteStartedAtMS:         run.DeleteStartedAtMS,
		CompletedAtMS:             run.CompletedAtMS,
		HasError:                  strings.TrimSpace(run.LastError) != "",
	}
}

func (m *archiveManager) status(ctx context.Context, runID string) (ArchiveStatus, error) {
	if !validArchiveRunID(runID) {
		return ArchiveStatus{}, ErrArchiveInvalidID
	}
	run, err := m.store.UsageArchives.Run(ctx, runID)
	if err != nil {
		return ArchiveStatus{}, err
	}
	segments, err := m.store.UsageArchives.Segments(ctx, runID)
	if err != nil {
		return ArchiveStatus{}, err
	}
	return ArchiveStatus{Run: run, Segments: segments}, nil
}

func (m *archiveManager) archiveLocked(ctx context.Context, runID string) (ArchiveStatus, error) {
	if !validArchiveRunID(runID) {
		return ArchiveStatus{}, ErrArchiveInvalidID
	}
	current, err := m.store.UsageArchives.Run(ctx, runID)
	if err != nil {
		return ArchiveStatus{}, err
	}
	if current.Mode == usagearchive.RunModeManual {
		if err := m.ensureManualArchiveReadiness(ctx); err != nil {
			return ArchiveStatus{}, err
		}
	}
	run, err := m.store.UsageArchives.BeginArchive(ctx, runID, time.Now().UnixMilli())
	if err != nil {
		return ArchiveStatus{}, err
	}
	if err := m.ensureRunDirectory(run.ID); err != nil {
		return ArchiveStatus{}, m.recordFailure(ctx, run.ID, usagearchive.StatusArchiving, err)
	}
	segments, err := m.store.UsageArchives.Segments(ctx, run.ID)
	if err != nil {
		return ArchiveStatus{}, m.recordFailure(ctx, run.ID, usagearchive.StatusArchiving, err)
	}
	nextSequence := len(segments) + 1
	afterEventID := run.LastArchivedEventID
	for run.ArchivedEventCount < run.EventCount {
		if err := ctx.Err(); err != nil {
			return ArchiveStatus{}, m.recordFailure(ctx, run.ID, usagearchive.StatusArchiving, err)
		}
		records, err := m.store.UsageArchives.Records(
			ctx,
			run.ID,
			afterEventID,
			m.config.SegmentEventLimit,
			m.config.SegmentByteLimit,
		)
		if err != nil {
			return ArchiveStatus{}, m.recordFailure(ctx, run.ID, usagearchive.StatusArchiving, err)
		}
		if len(records) == 0 {
			err := fmt.Errorf("%w: archive stopped after %d of %d events", ErrArchiveCoverageIncomplete, run.ArchivedEventCount, run.EventCount)
			return ArchiveStatus{}, m.recordFailure(ctx, run.ID, usagearchive.StatusArchiving, err)
		}
		segment, refs, err := m.writeSegment(run.ID, nextSequence, records)
		if err != nil {
			return ArchiveStatus{}, m.recordFailure(ctx, run.ID, usagearchive.StatusArchiving, err)
		}
		if err := m.callTestHook("segment_published"); err != nil {
			return ArchiveStatus{}, m.recordFailure(ctx, run.ID, usagearchive.StatusArchiving, err)
		}
		run, err = m.store.UsageArchives.RecordSegment(ctx, run.ID, segment, refs, time.Now().UnixMilli())
		if err != nil {
			return ArchiveStatus{}, m.recordFailure(ctx, run.ID, usagearchive.StatusArchiving, err)
		}
		afterEventID = run.LastArchivedEventID
		nextSequence++
	}
	segments, err = m.store.UsageArchives.Segments(ctx, run.ID)
	if err != nil {
		return ArchiveStatus{}, m.recordFailure(ctx, run.ID, usagearchive.StatusArchiving, err)
	}
	generatedAtMS := time.Now().UnixMilli()
	manifest, err := m.buildManifest(ctx, run, segments, generatedAtMS)
	if err != nil {
		return ArchiveStatus{}, m.recordFailure(ctx, run.ID, usagearchive.StatusArchiving, err)
	}
	manifestFile, manifestSHA256, err := m.writeManifest(run.ID, manifest)
	if err != nil {
		return ArchiveStatus{}, m.recordFailure(ctx, run.ID, usagearchive.StatusArchiving, err)
	}
	if err := m.callTestHook("manifest_published"); err != nil {
		return ArchiveStatus{}, m.recordFailure(ctx, run.ID, usagearchive.StatusArchiving, err)
	}
	run, err = m.store.UsageArchives.MarkArchived(
		ctx,
		run.ID,
		manifest.EventHashDigest,
		manifestFile,
		manifestSHA256,
		generatedAtMS,
	)
	if err != nil {
		return ArchiveStatus{}, m.recordFailure(ctx, run.ID, usagearchive.StatusArchiving, err)
	}
	return ArchiveStatus{Run: run, Segments: segments}, nil
}

func (m *archiveManager) ensureManualArchiveReadiness(ctx context.Context) error {
	ready, err := m.store.UsageCacheAccountingMigrationReady(ctx)
	if err != nil {
		return fmt.Errorf("inspect usage cache accounting migration before archive: %w", err)
	}
	if !ready {
		return fmt.Errorf("%w: usage cache accounting migration is not complete", ErrArchiveCoverageIncomplete)
	}
	for {
		updated, err := m.store.BackfillUsageResponseMetadata(ctx, archiveReadinessBackfillBatch)
		if err != nil {
			return fmt.Errorf("backfill usage response metadata before archive: %w", err)
		}
		if updated == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

func (m *archiveManager) verifyLocked(ctx context.Context, runID string) (ArchiveStatus, error) {
	status, err := m.status(ctx, runID)
	if err != nil {
		return ArchiveStatus{}, err
	}
	switch status.Run.Status {
	case usagearchive.StatusVerified, usagearchive.StatusDeleting, usagearchive.StatusCompleted:
		return status, nil
	case usagearchive.StatusArchived, usagearchive.StatusVerifying:
	case usagearchive.StatusFailed:
		if status.Run.ResumeStatus != usagearchive.StatusVerifying {
			return ArchiveStatus{}, fmt.Errorf("%w: cannot verify failed %s stage", ErrArchiveInvalidState, status.Run.ResumeStatus)
		}
	default:
		return ArchiveStatus{}, fmt.Errorf("%w: cannot verify run in %s", ErrArchiveInvalidState, status.Run.Status)
	}
	run, err := m.store.UsageArchives.BeginVerification(ctx, runID, time.Now().UnixMilli())
	if err != nil {
		return ArchiveStatus{}, err
	}
	segments, err := m.store.UsageArchives.Segments(ctx, run.ID)
	if err != nil {
		return ArchiveStatus{}, m.recordFailure(ctx, run.ID, usagearchive.StatusVerifying, err)
	}
	if err := m.verifyManifest(ctx, run, segments); err != nil {
		return ArchiveStatus{}, m.recordFailure(ctx, run.ID, usagearchive.StatusVerifying, err)
	}
	run, err = m.store.UsageArchives.MarkVerified(ctx, run.ID, time.Now().UnixMilli())
	if err != nil {
		return ArchiveStatus{}, m.recordFailure(ctx, run.ID, usagearchive.StatusVerifying, err)
	}
	segments, err = m.store.UsageArchives.Segments(ctx, run.ID)
	if err != nil {
		return ArchiveStatus{}, err
	}
	return ArchiveStatus{Run: run, Segments: segments}, nil
}

func (m *archiveManager) deleteLocked(ctx context.Context, runID string) (ArchiveStatus, error) {
	status, err := m.status(ctx, runID)
	if err != nil {
		return ArchiveStatus{}, err
	}
	if status.Run.Status == usagearchive.StatusCompleted {
		return status, nil
	}
	if !m.config.AggregateReadsEnabled {
		return ArchiveStatus{}, ErrArchiveDeleteUnavailable
	}
	switch status.Run.Status {
	case usagearchive.StatusVerified, usagearchive.StatusDeleting:
	case usagearchive.StatusFailed:
		if status.Run.ResumeStatus != usagearchive.StatusDeleting {
			return ArchiveStatus{}, fmt.Errorf("%w: cannot delete failed %s stage", ErrArchiveInvalidState, status.Run.ResumeStatus)
		}
	default:
		return ArchiveStatus{}, fmt.Errorf("%w: cannot delete run in %s", ErrArchiveInvalidState, status.Run.Status)
	}
	run, err := m.store.UsageArchives.BeginDelete(ctx, runID, time.Now().UnixMilli())
	if err != nil {
		return ArchiveStatus{}, err
	}
	for run.Status == usagearchive.StatusDeleting {
		if err := ctx.Err(); err != nil {
			return ArchiveStatus{}, m.recordFailure(ctx, run.ID, usagearchive.StatusDeleting, err)
		}
		result, err := m.store.UsageArchives.DeleteBatch(ctx, run.ID, m.config.DeleteBatchSize, time.Now().UnixMilli())
		if err != nil {
			return ArchiveStatus{}, m.recordFailure(ctx, run.ID, usagearchive.StatusDeleting, err)
		}
		run = result.Run
		if !result.Completed {
			if err := m.callTestHook("delete_batch_committed"); err != nil {
				return ArchiveStatus{}, m.recordFailure(ctx, run.ID, usagearchive.StatusDeleting, err)
			}
		}
		if result.Completed {
			break
		}
	}
	segments, err := m.store.UsageArchives.Segments(ctx, run.ID)
	if err != nil {
		return ArchiveStatus{}, err
	}
	return ArchiveStatus{Run: run, Segments: segments}, nil
}

func (m *archiveManager) recordFailure(ctx context.Context, runID, resumeStatus string, failure error) error {
	if failure == nil {
		return nil
	}
	failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := m.store.UsageArchives.RecordFailure(failureCtx, runID, resumeStatus, failure, time.Now().UnixMilli()); err != nil {
		return errors.Join(failure, fmt.Errorf("record usage archive failure: %w", err))
	}
	return failure
}

func (m *archiveManager) writeSegment(runID string, sequence int, records []store.UsageArchiveRecord) (store.UsageArchiveSegment, []store.UsageArchiveRecordRef, error) {
	if len(records) == 0 {
		return store.UsageArchiveSegment{}, nil, errors.New("usage archive segment is empty")
	}
	firstEventID := records[0].EventID
	lastEventID := records[len(records)-1].EventID
	baseName := fmt.Sprintf("segment-%06d-%020d-%020d.jsonl.gz", sequence, firstEventID, lastEventID)
	relativeName := filepath.ToSlash(filepath.Join(runID, baseName))
	path, err := m.resolveArchivePath(relativeName)
	if err != nil {
		return store.UsageArchiveSegment{}, nil, err
	}
	segmentDigest := sha256.New()
	refs := make([]store.UsageArchiveRecordRef, 0, len(records))
	minTimestampMS := records[0].TimestampMS
	maxTimestampMS := records[0].TimestampMS
	var uncompressedBytes int64
	contentSHA256 := sha256.New()
	compressedBytes, err := atomicWritePrivate(path, func(writer io.Writer) error {
		zipper, err := gzip.NewWriterLevel(io.MultiWriter(writer, contentSHA256), gzip.BestSpeed)
		if err != nil {
			return err
		}
		zipper.Header.ModTime = time.Time{}
		zipper.Header.OS = 255
		buffer := bufio.NewWriterSize(zipper, 64*1024)
		for index, record := range records {
			if record.EventID <= 0 || strings.TrimSpace(record.EventHash) == "" || record.TimestampMS <= 0 {
				_ = zipper.Close()
				return fmt.Errorf("usage archive event %d has an invalid identity", record.EventID)
			}
			if index > 0 && record.EventID <= records[index-1].EventID {
				_ = zipper.Close()
				return errors.New("usage archive records are not ordered")
			}
			if len(record.Payload) > maxArchiveRecordBytes {
				_ = zipper.Close()
				return fmt.Errorf("usage archive event %d payload exceeds %d bytes", record.EventID, maxArchiveRecordBytes)
			}
			if !json.Valid(record.Payload) {
				_ = zipper.Close()
				return fmt.Errorf("usage archive event %d has invalid JSON payload", record.EventID)
			}
			if _, err := buffer.Write(record.Payload); err != nil {
				_ = zipper.Close()
				return err
			}
			if err := buffer.WriteByte('\n'); err != nil {
				_ = zipper.Close()
				return err
			}
			uncompressedBytes += int64(len(record.Payload) + 1)
			if uncompressedBytes > m.config.SegmentByteLimit {
				_ = zipper.Close()
				return fmt.Errorf("usage archive segment %d exceeds %d uncompressed bytes", sequence, m.config.SegmentByteLimit)
			}
			appendArchiveEventDigest(segmentDigest, record.EventID, record.EventHash)
			refs = append(refs, store.UsageArchiveRecordRef{EventID: record.EventID, EventHash: record.EventHash})
			minTimestampMS = min(minTimestampMS, record.TimestampMS)
			maxTimestampMS = max(maxTimestampMS, record.TimestampMS)
		}
		if err := buffer.Flush(); err != nil {
			_ = zipper.Close()
			return err
		}
		return zipper.Close()
	})
	if err != nil {
		return store.UsageArchiveSegment{}, nil, fmt.Errorf("publish usage archive segment %d: %w", sequence, err)
	}
	return store.UsageArchiveSegment{
		RunID:             runID,
		Sequence:          sequence,
		Status:            usagearchive.SegmentStatusPublished,
		FileName:          relativeName,
		FirstEventID:      firstEventID,
		LastEventID:       lastEventID,
		MinTimestampMS:    minTimestampMS,
		MaxTimestampMS:    maxTimestampMS,
		EventCount:        int64(len(records)),
		UncompressedBytes: uncompressedBytes,
		CompressedBytes:   compressedBytes,
		ContentSHA256:     hex.EncodeToString(contentSHA256.Sum(nil)),
		EventHashDigest:   hex.EncodeToString(segmentDigest.Sum(nil)),
	}, refs, nil
}

func (m *archiveManager) buildManifest(ctx context.Context, run store.UsageArchiveRun, segments []store.UsageArchiveSegment, generatedAtMS int64) (ArchiveManifest, error) {
	if len(segments) == 0 {
		return ArchiveManifest{}, fmt.Errorf("%w: archive has no segments", ErrArchiveCoverageIncomplete)
	}
	overallDigest := sha256.New()
	manifestSegments := make([]ArchiveManifestSegment, 0, len(segments))
	var eventCount, uncompressedBytes, compressedBytes int64
	var previousLastEventID int64
	var firstEventID, lastEventID, minTimestampMS, maxTimestampMS int64
	for index, segment := range segments {
		if err := ctx.Err(); err != nil {
			return ArchiveManifest{}, err
		}
		if segment.Sequence != index+1 || (index > 0 && segment.FirstEventID <= previousLastEventID) {
			return ArchiveManifest{}, fmt.Errorf("%w: archive segment ordering is invalid at sequence %d", ErrArchiveCoverageIncomplete, segment.Sequence)
		}
		inspection, err := m.inspectSegment(segment, overallDigest)
		if err != nil {
			return ArchiveManifest{}, err
		}
		if err := compareSegmentInspection(segment, inspection); err != nil {
			return ArchiveManifest{}, err
		}
		eventCount += inspection.EventCount
		uncompressedBytes += inspection.UncompressedBytes
		compressedBytes += inspection.CompressedBytes
		if index == 0 {
			firstEventID = inspection.FirstEventID
			minTimestampMS = inspection.MinTimestampMS
			maxTimestampMS = inspection.MaxTimestampMS
		}
		lastEventID = inspection.LastEventID
		minTimestampMS = min(minTimestampMS, inspection.MinTimestampMS)
		maxTimestampMS = max(maxTimestampMS, inspection.MaxTimestampMS)
		manifestSegments = append(manifestSegments, manifestSegment(segment))
		previousLastEventID = segment.LastEventID
	}
	if eventCount != run.EventCount || uncompressedBytes != run.ArchivedUncompressedBytes || compressedBytes != run.ArchivedCompressedBytes {
		return ArchiveManifest{}, fmt.Errorf(
			"%w: manifest totals events=%d/%d uncompressed=%d/%d compressed=%d/%d",
			ErrArchiveCoverageIncomplete,
			eventCount,
			run.EventCount,
			uncompressedBytes,
			run.ArchivedUncompressedBytes,
			compressedBytes,
			run.ArchivedCompressedBytes,
		)
	}
	return ArchiveManifest{
		SchemaVersion:     run.SchemaVersion,
		Format:            run.Format,
		RunID:             run.ID,
		Mode:              run.Mode,
		CutoffTimestampMS: run.CutoffTimestampMS,
		FirstEventID:      firstEventID,
		LastEventID:       lastEventID,
		MinTimestampMS:    minTimestampMS,
		MaxTimestampMS:    maxTimestampMS,
		TargetEventID:     run.TargetEventID,
		EventCount:        eventCount,
		UncompressedBytes: uncompressedBytes,
		CompressedBytes:   compressedBytes,
		EventHashDigest:   hex.EncodeToString(overallDigest.Sum(nil)),
		GeneratedAtMS:     generatedAtMS,
		Segments:          manifestSegments,
	}, nil
}

func (m *archiveManager) writeManifest(runID string, manifest ArchiveManifest) (string, string, error) {
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", "", err
	}
	payload = append(payload, '\n')
	if int64(len(payload)) > maxArchiveManifestBytes {
		return "", "", fmt.Errorf("usage archive manifest exceeds %d bytes", maxArchiveManifestBytes)
	}
	relativeName := filepath.ToSlash(filepath.Join(runID, "manifest.json"))
	path, err := m.resolveArchivePath(relativeName)
	if err != nil {
		return "", "", err
	}
	if _, err := atomicWritePrivate(path, func(writer io.Writer) error {
		_, err := writer.Write(payload)
		return err
	}); err != nil {
		return "", "", fmt.Errorf("publish usage archive manifest: %w", err)
	}
	digest := sha256.Sum256(payload)
	return relativeName, hex.EncodeToString(digest[:]), nil
}

func (m *archiveManager) verifyManifest(ctx context.Context, run store.UsageArchiveRun, segments []store.UsageArchiveSegment) error {
	if len(segments) == 0 {
		return fmt.Errorf("%w: archive has no segments", ErrArchiveCoverageIncomplete)
	}
	manifestPath, err := m.resolveArchivePath(run.ManifestFile)
	if err != nil {
		return err
	}
	file, info, err := openPrivateRegularFile(m.config.Directory, manifestPath)
	if err != nil {
		return fmt.Errorf("inspect usage archive manifest: %w", err)
	}
	defer file.Close()
	if info.Size() <= 0 || info.Size() > maxArchiveManifestBytes {
		return fmt.Errorf("usage archive manifest size %d is invalid", info.Size())
	}
	payload, err := io.ReadAll(io.LimitReader(file, maxArchiveManifestBytes+1))
	if err != nil {
		return fmt.Errorf("read usage archive manifest: %w", err)
	}
	if int64(len(payload)) != info.Size() {
		return errors.New("usage archive manifest size changed while reading")
	}
	if err := ensureOpenFileStillPublished(m.config.Directory, manifestPath, info); err != nil {
		return fmt.Errorf("validate usage archive manifest path: %w", err)
	}
	manifestDigest := sha256.Sum256(payload)
	if hex.EncodeToString(manifestDigest[:]) != run.ManifestSHA256 {
		return errors.New("usage archive manifest checksum mismatch")
	}
	var manifest ArchiveManifest
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return fmt.Errorf("decode usage archive manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("usage archive manifest contains multiple JSON values")
		}
		return fmt.Errorf("decode usage archive manifest suffix: %w", err)
	}
	if manifest.SchemaVersion != run.SchemaVersion || manifest.Format != run.Format || manifest.RunID != run.ID || manifest.Mode != run.Mode ||
		manifest.CutoffTimestampMS != run.CutoffTimestampMS || manifest.TargetEventID != run.TargetEventID ||
		manifest.FirstEventID != segments[0].FirstEventID || manifest.LastEventID != segments[len(segments)-1].LastEventID ||
		manifest.EventCount != run.EventCount || manifest.UncompressedBytes != run.ArchivedUncompressedBytes ||
		manifest.CompressedBytes != run.ArchivedCompressedBytes || manifest.EventHashDigest != run.ArchiveDigest ||
		manifest.GeneratedAtMS != run.ArchivedAtMS || len(manifest.Segments) != len(segments) {
		return errors.New("usage archive manifest does not match run metadata")
	}
	overallDigest := sha256.New()
	var previousLastEventID int64
	var minTimestampMS, maxTimestampMS int64
	for index, segment := range segments {
		if err := ctx.Err(); err != nil {
			return err
		}
		if segment.Sequence != index+1 || (index > 0 && segment.FirstEventID <= previousLastEventID) {
			return fmt.Errorf("usage archive segment ordering is invalid at sequence %d", segment.Sequence)
		}
		if manifest.Segments[index] != manifestSegment(segment) {
			return fmt.Errorf("usage archive manifest segment %d does not match repository metadata", segment.Sequence)
		}
		inspection, err := m.inspectSegment(segment, overallDigest)
		if err != nil {
			return err
		}
		if err := compareSegmentInspection(segment, inspection); err != nil {
			return err
		}
		if index == 0 {
			minTimestampMS = inspection.MinTimestampMS
			maxTimestampMS = inspection.MaxTimestampMS
		}
		minTimestampMS = min(minTimestampMS, inspection.MinTimestampMS)
		maxTimestampMS = max(maxTimestampMS, inspection.MaxTimestampMS)
		previousLastEventID = segment.LastEventID
	}
	if manifest.MinTimestampMS != minTimestampMS || manifest.MaxTimestampMS != maxTimestampMS {
		return errors.New("usage archive manifest time range does not match segments")
	}
	if hex.EncodeToString(overallDigest.Sum(nil)) != run.ArchiveDigest {
		return errors.New("usage archive event hash digest mismatch")
	}
	return nil
}

func (m *archiveManager) inspectSegment(segment store.UsageArchiveSegment, overallDigest hash.Hash) (archiveFileInspection, error) {
	path, err := m.resolveArchivePath(segment.FileName)
	if err != nil {
		return archiveFileInspection{}, err
	}
	file, info, err := openPrivateRegularFile(m.config.Directory, path)
	if err != nil {
		return archiveFileInspection{}, fmt.Errorf("inspect usage archive segment %d: %w", segment.Sequence, err)
	}
	defer file.Close()
	contentDigest := sha256.New()
	copied, copyErr := io.Copy(contentDigest, file)
	if copyErr != nil {
		return archiveFileInspection{}, copyErr
	}
	if copied != info.Size() {
		return archiveFileInspection{}, errors.New("usage archive segment size changed during checksum")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return archiveFileInspection{}, err
	}
	zipper, err := gzip.NewReader(file)
	if err != nil {
		return archiveFileInspection{}, fmt.Errorf("open usage archive segment %d: %w", segment.Sequence, err)
	}
	defer zipper.Close()
	scanner := bufio.NewScanner(zipper)
	scanner.Buffer(make([]byte, 64*1024), maxArchiveRecordBytes+1)
	segmentDigest := sha256.New()
	inspection := archiveFileInspection{
		CompressedBytes: copied,
		ContentSHA256:   hex.EncodeToString(contentDigest.Sum(nil)),
	}
	for scanner.Scan() {
		payload := scanner.Bytes()
		if len(payload) == 0 {
			return archiveFileInspection{}, fmt.Errorf("usage archive segment %d contains an empty record", segment.Sequence)
		}
		var envelope archiveRecordEnvelope
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return archiveFileInspection{}, fmt.Errorf("decode usage archive segment %d record %d: %w", segment.Sequence, inspection.EventCount+1, err)
		}
		if envelope.SchemaVersion != usagearchive.SchemaVersion || envelope.EventID <= 0 || strings.TrimSpace(envelope.EventHash) == "" || envelope.TimestampMS <= 0 {
			return archiveFileInspection{}, fmt.Errorf("usage archive segment %d contains an invalid record envelope", segment.Sequence)
		}
		parsed, err := usageparser.ParseImportPayload(payload)
		if err != nil {
			return archiveFileInspection{}, fmt.Errorf("usage archive segment %d contains an unrestorable record: %w", segment.Sequence, err)
		}
		if parsed.Format != usageparser.ImportFormatJSONL || parsed.Failed != 0 || parsed.Unsupported != 0 || len(parsed.Events) != 1 {
			return archiveFileInspection{}, fmt.Errorf("usage archive segment %d contains an unrestorable record", segment.Sequence)
		}
		restored := parsed.Events[0]
		if !restored.PreserveArchiveDerivedFields || restored.EventHash != envelope.EventHash || restored.TimestampMS != envelope.TimestampMS {
			return archiveFileInspection{}, fmt.Errorf("usage archive segment %d record envelope does not match restored event", segment.Sequence)
		}
		if inspection.EventCount > 0 && envelope.EventID <= inspection.LastEventID {
			return archiveFileInspection{}, fmt.Errorf("usage archive segment %d event ids are not ordered", segment.Sequence)
		}
		if inspection.EventCount == 0 {
			inspection.FirstEventID = envelope.EventID
			inspection.MinTimestampMS = envelope.TimestampMS
			inspection.MaxTimestampMS = envelope.TimestampMS
		}
		inspection.LastEventID = envelope.EventID
		inspection.MinTimestampMS = min(inspection.MinTimestampMS, envelope.TimestampMS)
		inspection.MaxTimestampMS = max(inspection.MaxTimestampMS, envelope.TimestampMS)
		inspection.EventCount++
		inspection.UncompressedBytes += int64(len(payload) + 1)
		if inspection.UncompressedBytes > maxArchiveSegmentBytes {
			return archiveFileInspection{}, fmt.Errorf("usage archive segment %d exceeds %d uncompressed bytes", segment.Sequence, maxArchiveSegmentBytes)
		}
		appendArchiveEventDigest(segmentDigest, envelope.EventID, envelope.EventHash)
		appendArchiveEventDigest(overallDigest, envelope.EventID, envelope.EventHash)
	}
	if err := scanner.Err(); err != nil {
		return archiveFileInspection{}, fmt.Errorf("scan usage archive segment %d: %w", segment.Sequence, err)
	}
	if err := zipper.Close(); err != nil {
		return archiveFileInspection{}, fmt.Errorf("close usage archive segment %d: %w", segment.Sequence, err)
	}
	if err := ensureOpenFileStillPublished(m.config.Directory, path, info); err != nil {
		return archiveFileInspection{}, fmt.Errorf("validate usage archive segment %d path: %w", segment.Sequence, err)
	}
	inspection.EventHashDigest = hex.EncodeToString(segmentDigest.Sum(nil))
	return inspection, nil
}

func (m *archiveManager) ensureRunDirectory(runID string) error {
	if !validArchiveRunID(runID) {
		return ErrArchiveInvalidID
	}
	if err := ensurePrivateDirectory(m.config.Directory, true); err != nil {
		return fmt.Errorf("prepare usage archive directory: %w", err)
	}
	runDirectory := filepath.Join(m.config.Directory, runID)
	if err := ensurePrivateDirectory(runDirectory, false); err != nil {
		return fmt.Errorf("prepare usage archive run directory: %w", err)
	}
	return nil
}

func (m *archiveManager) resolveArchivePath(relativeName string) (string, error) {
	if strings.TrimSpace(relativeName) == "" || filepath.IsAbs(relativeName) {
		return "", errors.New("usage archive file name is invalid")
	}
	native := filepath.FromSlash(relativeName)
	clean := filepath.Clean(native)
	if clean != native || clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", errors.New("usage archive file name is unsafe")
	}
	path := filepath.Join(m.config.Directory, clean)
	relative, err := filepath.Rel(m.config.Directory, path)
	if err != nil || relative != clean {
		return "", errors.New("usage archive file escapes archive directory")
	}
	return path, nil
}

func (m *archiveManager) callTestHook(point string) error {
	if m.testHook == nil {
		return nil
	}
	return m.testHook(point)
}

func manifestSegment(segment store.UsageArchiveSegment) ArchiveManifestSegment {
	return ArchiveManifestSegment{
		Sequence:          segment.Sequence,
		FileName:          segment.FileName,
		FirstEventID:      segment.FirstEventID,
		LastEventID:       segment.LastEventID,
		MinTimestampMS:    segment.MinTimestampMS,
		MaxTimestampMS:    segment.MaxTimestampMS,
		EventCount:        segment.EventCount,
		UncompressedBytes: segment.UncompressedBytes,
		CompressedBytes:   segment.CompressedBytes,
		ContentSHA256:     segment.ContentSHA256,
		EventHashDigest:   segment.EventHashDigest,
	}
}

func compareSegmentInspection(segment store.UsageArchiveSegment, inspection archiveFileInspection) error {
	if inspection.EventCount != segment.EventCount || inspection.FirstEventID != segment.FirstEventID ||
		inspection.LastEventID != segment.LastEventID || inspection.MinTimestampMS != segment.MinTimestampMS ||
		inspection.MaxTimestampMS != segment.MaxTimestampMS || inspection.UncompressedBytes != segment.UncompressedBytes ||
		inspection.CompressedBytes != segment.CompressedBytes || inspection.ContentSHA256 != segment.ContentSHA256 ||
		inspection.EventHashDigest != segment.EventHashDigest {
		return fmt.Errorf("usage archive segment %d metadata mismatch", segment.Sequence)
	}
	return nil
}

func appendArchiveEventDigest(digest hash.Hash, eventID int64, eventHash string) {
	_, _ = io.WriteString(digest, strconv.FormatInt(eventID, 10))
	_, _ = digest.Write([]byte{0})
	_, _ = io.WriteString(digest, eventHash)
	_, _ = digest.Write([]byte{'\n'})
}

func atomicWritePrivate(path string, write func(io.Writer) error) (int64, error) {
	directory := filepath.Dir(path)
	if err := validatePrivateDirectory(directory); err != nil {
		return 0, err
	}
	temporary, err := os.CreateTemp(directory, ".archive-tmp-*")
	if err != nil {
		return 0, err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return 0, err
	}
	counting := &archiveCountingWriter{writer: temporary}
	if err := write(counting); err != nil {
		return 0, err
	}
	if err := temporary.Sync(); err != nil {
		return 0, err
	}
	if err := temporary.Close(); err != nil {
		return 0, err
	}
	if err := replaceArchiveFile(temporaryPath, path); err != nil {
		return 0, err
	}
	removeTemporary = false
	if err := syncDirectory(directory); err != nil {
		return 0, err
	}
	return counting.written, nil
}

type archiveCountingWriter struct {
	writer  io.Writer
	written int64
}

func (w *archiveCountingWriter) Write(payload []byte) (int, error) {
	written, err := w.writer.Write(payload)
	w.written += int64(written)
	return written, err
}

func ensurePrivateDirectory(path string, createParents bool) error {
	if err := rejectExistingSymlinkComponents(path); err != nil {
		return err
	}
	var err error
	if createParents {
		err = os.MkdirAll(path, 0o700)
	} else {
		err = os.Mkdir(path, 0o700)
		if errors.Is(err, os.ErrExist) {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	if err := validatePrivateDirectory(path); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func resolveArchiveDirectory(path string) (string, error) {
	parent := filepath.Dir(path)
	missing := make([]string, 0, 4)
	for {
		_, err := os.Lstat(parent)
		if err == nil {
			resolvedParent, resolveErr := filepath.EvalSymlinks(parent)
			if resolveErr != nil {
				return "", resolveErr
			}
			for index := len(missing) - 1; index >= 0; index-- {
				resolvedParent = filepath.Join(resolvedParent, missing[index])
			}
			return filepath.Join(resolvedParent, filepath.Base(path)), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", err
		}
		missing = append(missing, filepath.Base(parent))
		parent = next
	}
}

func rejectExistingSymlinkComponents(path string) error {
	current := filepath.Clean(path)
	for {
		info, err := os.Lstat(current)
		switch {
		case err == nil && info.Mode()&os.ModeSymlink != 0:
			return errors.New("archive path contains a symbolic link")
		case err != nil && !errors.Is(err, os.ErrNotExist):
			return err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current = parent
	}
}

func validatePrivateDirectory(path string) error {
	if err := rejectExistingSymlinkComponents(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("archive path is not a private directory")
	}
	return nil
}

func privateRegularFileInfo(baseDirectory, path string) (os.FileInfo, error) {
	if err := rejectSymlinkComponentsBelow(baseDirectory, path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("archive path is not a regular file")
	}
	if !archiveFilePermissionsPrivate(info) {
		return nil, errors.New("archive file permissions are not private")
	}
	return info, nil
}

func openPrivateRegularFile(baseDirectory, path string) (*os.File, os.FileInfo, error) {
	expected, err := privateRegularFileInfo(baseDirectory, path)
	if err != nil {
		return nil, nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	actual, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !actual.Mode().IsRegular() || !archiveFilePermissionsPrivate(actual) || !os.SameFile(expected, actual) {
		_ = file.Close()
		return nil, nil, errors.New("archive file changed while opening")
	}
	return file, actual, nil
}

func ensureOpenFileStillPublished(baseDirectory, path string, openInfo os.FileInfo) error {
	current, err := privateRegularFileInfo(baseDirectory, path)
	if err != nil {
		return err
	}
	if !os.SameFile(openInfo, current) {
		return errors.New("archive file changed during verification")
	}
	return nil
}

func rejectSymlinkComponentsBelow(baseDirectory, path string) error {
	baseDirectory = filepath.Clean(baseDirectory)
	path = filepath.Clean(path)
	relative, err := filepath.Rel(baseDirectory, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("archive file escapes archive directory")
	}
	current := baseDirectory
	components := []string{"."}
	if relative != "." {
		components = strings.Split(relative, string(filepath.Separator))
	}
	for _, component := range components {
		if component != "." {
			current = filepath.Join(current, component)
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("archive path contains a symbolic link")
		}
	}
	return nil
}

func newArchiveRunID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return hex.EncodeToString(random), nil
}

func validArchiveRunID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
