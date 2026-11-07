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
	"sync"
	"time"
)

const (
	WALCheckpointModePending    = "pending"
	WALCheckpointModePassive    = "passive"
	WALCheckpointModeTruncate   = "truncate"
	WALCheckpointModeUnobserved = "unobserved"
	WALJournalSizeLimitBytes = int64(256 << 20)

	defaultWALMaintenanceInterval = 30 * time.Second
	// PASSIVE checkpoints do not wait for readers or writers, but draining an
	// already-large WAL can still require sustained disk I/O. Keep a generous
	// safety deadline so multi-gigabyte recovery is not interrupted after a
	// fraction of a second; shutdown still cancels the parent context promptly.
	defaultWALMaintenanceTimeout       = 10 * time.Minute
	defaultWALMaintenanceBusyTimeoutMS = 250
	defaultWALTruncateAttemptInterval  = 5 * time.Minute
	defaultWALTruncateThresholdBytes   = WALJournalSizeLimitBytes
)

type WALCheckpointSnapshot struct {
	Mode                    string `json:"mode"`
	Busy                    int64  `json:"busy"`
	LogFrames               int64  `json:"logFrames"`
	CheckpointedFrames      int64  `json:"checkpointedFrames"`
	ExecutedAtMS            int64  `json:"executedAtMs"`
	DurationMS              int64  `json:"durationMs"`
	LastTruncateAttemptAtMS int64  `json:"lastTruncateAttemptAtMs,omitempty"`
	Error                   string `json:"error,omitempty"`
}

type WALMaintenanceSnapshot struct {
	DatabaseBytes         int64                 `json:"databaseBytes"`
	WALBytes              int64                 `json:"walBytes"`
	SHMBytes              int64                 `json:"shmBytes"`
	TotalBytes            int64                 `json:"totalBytes"`
	JournalSizeLimitBytes int64                 `json:"journalSizeLimitBytes"`
	Checkpoint            WALCheckpointSnapshot `json:"checkpoint"`
}

type databaseFileStats struct {
	DatabaseBytes int64
	WALBytes      int64
	SHMBytes      int64
	TotalBytes    int64
}

type walMaintenanceOptions struct {
	interval                time.Duration
	operationTimeout        time.Duration
	truncateAttemptInterval time.Duration
	truncateThresholdBytes  int64
	journalSizeLimitBytes   int64
	now                     func() time.Time
}

type WALMaintenance struct {
	db      *sql.DB
	dbPath  string
	options walMaintenanceOptions

	runMu               sync.Mutex
	lastTruncateAttempt time.Time

	snapshotMu sync.RWMutex
	snapshot   WALMaintenanceSnapshot

	lifecycleMu sync.Mutex
	started     bool
	closed      bool
	cancel      context.CancelFunc
	closeDone   chan struct{}
	closeErr    error
	wg          sync.WaitGroup
}

func NewWALMaintenance(path string) (*WALMaintenance, error) {
	return newWALMaintenance(path, walMaintenanceOptions{})
}

// InspectDatabaseFiles returns a point-in-time file-size snapshot for path
// without opening an additional SQLite connection.
func InspectDatabaseFiles(path string) WALMaintenanceSnapshot {
	files, fileErr := readDatabaseFileStats(path)
	checkpoint := WALCheckpointSnapshot{Mode: WALCheckpointModeUnobserved}
	if fileErr != nil {
		checkpoint.Error = fileErr.Error()
	}
	return WALMaintenanceSnapshot{
		DatabaseBytes:         files.DatabaseBytes,
		WALBytes:              files.WALBytes,
		SHMBytes:              files.SHMBytes,
		TotalBytes:            files.TotalBytes,
		JournalSizeLimitBytes: WALJournalSizeLimitBytes,
		Checkpoint:            checkpoint,
	}
}

func newWALMaintenance(path string, options walMaintenanceOptions) (*WALMaintenance, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("SQLite maintenance path is empty")
	}
	dbPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve SQLite maintenance path: %w", err)
	}
	db, err := sql.Open("sqlite", walMaintenanceDataSourceName(dbPath))
	if err != nil {
		return nil, fmt.Errorf("configure SQLite WAL maintenance connection: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	options = normalizeWALMaintenanceOptions(options)
	files, fileErr := readDatabaseFileStats(dbPath)
	checkpoint := WALCheckpointSnapshot{Mode: WALCheckpointModePending}
	if fileErr != nil {
		checkpoint.Error = fileErr.Error()
	}
	return &WALMaintenance{
		db:      db,
		dbPath:  dbPath,
		options: options,
		snapshot: WALMaintenanceSnapshot{
			DatabaseBytes:         files.DatabaseBytes,
			WALBytes:              files.WALBytes,
			SHMBytes:              files.SHMBytes,
			TotalBytes:            files.TotalBytes,
			JournalSizeLimitBytes: options.journalSizeLimitBytes,
			Checkpoint:            checkpoint,
		},
		closeDone: make(chan struct{}),
	}, nil
}

func normalizeWALMaintenanceOptions(options walMaintenanceOptions) walMaintenanceOptions {
	if options.interval <= 0 {
		options.interval = defaultWALMaintenanceInterval
	}
	if options.operationTimeout <= 0 {
		options.operationTimeout = defaultWALMaintenanceTimeout
	}
	if options.truncateAttemptInterval <= 0 {
		options.truncateAttemptInterval = defaultWALTruncateAttemptInterval
	}
	if options.truncateThresholdBytes <= 0 {
		options.truncateThresholdBytes = defaultWALTruncateThresholdBytes
	}
	if options.journalSizeLimitBytes <= 0 {
		options.journalSizeLimitBytes = WALJournalSizeLimitBytes
	}
	if options.now == nil {
		options.now = time.Now
	}
	return options
}

func (m *WALMaintenance) Start(parent context.Context) {
	if m == nil {
		return
	}
	m.lifecycleMu.Lock()
	if m.started || m.closed {
		m.lifecycleMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	m.started = true
	m.cancel = cancel
	m.wg.Add(1)
	m.lifecycleMu.Unlock()

	go func() {
		defer m.wg.Done()
		m.runWithTimeout(ctx)
		ticker := time.NewTicker(m.options.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.runWithTimeout(ctx)
			}
		}
	}()
}

func (m *WALMaintenance) Snapshot() WALMaintenanceSnapshot {
	if m == nil {
		return WALMaintenanceSnapshot{}
	}
	m.snapshotMu.RLock()
	defer m.snapshotMu.RUnlock()
	return m.snapshot
}

func (m *WALMaintenance) Close() error {
	if m == nil {
		return nil
	}
	m.lifecycleMu.Lock()
	if m.closed {
		done := m.closeDone
		m.lifecycleMu.Unlock()
		<-done
		m.lifecycleMu.Lock()
		err := m.closeErr
		m.lifecycleMu.Unlock()
		return err
	}
	m.closed = true
	cancel := m.cancel
	m.lifecycleMu.Unlock()

	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
	err := m.db.Close()

	m.lifecycleMu.Lock()
	m.closeErr = err
	close(m.closeDone)
	m.lifecycleMu.Unlock()
	return err
}

func (m *WALMaintenance) runWithTimeout(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, m.options.operationTimeout)
	defer cancel()
	m.runOnce(ctx)
}

func (m *WALMaintenance) runOnce(ctx context.Context) {
	m.runMu.Lock()
	defer m.runMu.Unlock()

	startedAt := time.Now()
	executedAt := m.options.now()
	checkpoint := WALCheckpointSnapshot{
		Mode:         WALCheckpointModePassive,
		ExecutedAtMS: executedAt.UnixMilli(),
	}
	if !m.lastTruncateAttempt.IsZero() {
		checkpoint.LastTruncateAttemptAtMS = m.lastTruncateAttempt.UnixMilli()
	}

	var runErr error
	conn, err := m.db.Conn(ctx)
	if err != nil {
		runErr = fmt.Errorf("open SQLite WAL maintenance connection: %w", err)
	} else {
		var appliedLimit int64
		query := fmt.Sprintf("pragma journal_size_limit = %d", m.options.journalSizeLimitBytes)
		if err := conn.QueryRowContext(ctx, query).Scan(&appliedLimit); err != nil {
			runErr = fmt.Errorf("set SQLite journal size limit: %w", err)
		} else if appliedLimit != m.options.journalSizeLimitBytes {
			runErr = fmt.Errorf(
				"set SQLite journal size limit: applied %d bytes, want %d",
				appliedLimit,
				m.options.journalSizeLimitBytes,
			)
		}
		if runErr == nil {
			checkpoint.Busy, checkpoint.LogFrames, checkpoint.CheckpointedFrames, runErr = runWALCheckpoint(
				ctx,
				conn,
				WALCheckpointModePassive,
			)
		}
	}

	files, fileErr := readDatabaseFileStats(m.dbPath)
	runErr = errors.Join(runErr, fileErr)
	if runErr == nil &&
		files.WALBytes > m.options.truncateThresholdBytes &&
		walCheckpointCaughtUp(checkpoint) &&
		(m.lastTruncateAttempt.IsZero() || executedAt.Sub(m.lastTruncateAttempt) >= m.options.truncateAttemptInterval) {
		m.lastTruncateAttempt = executedAt
		checkpoint.Mode = WALCheckpointModeTruncate
		checkpoint.LastTruncateAttemptAtMS = executedAt.UnixMilli()
		checkpoint.Busy, checkpoint.LogFrames, checkpoint.CheckpointedFrames, runErr = runWALCheckpoint(
			ctx,
			conn,
			WALCheckpointModeTruncate,
		)
		if runErr == nil && checkpoint.Busy != 0 {
			runErr = fmt.Errorf("truncate SQLite WAL checkpoint reported busy status %d", checkpoint.Busy)
		}
		files, fileErr = readDatabaseFileStats(m.dbPath)
		runErr = errors.Join(runErr, fileErr)
	}
	if conn != nil {
		runErr = errors.Join(runErr, conn.Close())
	}

	checkpoint.DurationMS = time.Since(startedAt).Milliseconds()
	if runErr != nil {
		checkpoint.Error = runErr.Error()
	}
	m.snapshotMu.Lock()
	m.snapshot = WALMaintenanceSnapshot{
		DatabaseBytes:         files.DatabaseBytes,
		WALBytes:              files.WALBytes,
		SHMBytes:              files.SHMBytes,
		TotalBytes:            files.TotalBytes,
		JournalSizeLimitBytes: m.options.journalSizeLimitBytes,
		Checkpoint:            checkpoint,
	}
	m.snapshotMu.Unlock()
}

func walCheckpointCaughtUp(checkpoint WALCheckpointSnapshot) bool {
	return checkpoint.Busy == 0 &&
		checkpoint.LogFrames >= 0 &&
		checkpoint.CheckpointedFrames == checkpoint.LogFrames
}

func walMaintenanceDataSourceName(path string) string {
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := &url.URL{Scheme: "file", Path: uriPath}
	query := dsn.Query()
	query.Set("mode", "rw")
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", defaultWALMaintenanceBusyTimeoutMS))
	query.Add("_pragma", "synchronous(FULL)")
	dsn.RawQuery = query.Encode()
	return dsn.String()
}

func runWALCheckpoint(ctx context.Context, conn *sql.Conn, mode string) (busy, logFrames, checkpointedFrames int64, err error) {
	switch mode {
	case WALCheckpointModePassive, WALCheckpointModeTruncate:
	default:
		return 0, 0, 0, fmt.Errorf("unsupported SQLite WAL checkpoint mode %q", mode)
	}
	err = conn.QueryRowContext(ctx, "pragma wal_checkpoint("+mode+")").Scan(
		&busy,
		&logFrames,
		&checkpointedFrames,
	)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("run SQLite WAL checkpoint %s: %w", mode, err)
	}
	return busy, logFrames, checkpointedFrames, nil
}

func readDatabaseFileStats(dbPath string) (databaseFileStats, error) {
	databaseBytes, databaseErr := databaseFileSize(dbPath, false)
	walBytes, walErr := databaseFileSize(dbPath+"-wal", true)
	shmBytes, shmErr := databaseFileSize(dbPath+"-shm", true)
	stats := databaseFileStats{
		DatabaseBytes: databaseBytes,
		WALBytes:      walBytes,
		SHMBytes:      shmBytes,
	}
	stats.TotalBytes = stats.DatabaseBytes + stats.WALBytes + stats.SHMBytes
	return stats, errors.Join(databaseErr, walErr, shmErr)
}

func databaseFileSize(path string, allowMissing bool) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		if allowMissing && os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("inspect SQLite file %s: %w", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("SQLite path is not a regular file: %s", filepath.Base(path))
	}
	return info.Size(), nil
}
