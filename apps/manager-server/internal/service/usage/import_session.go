package usage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type ImportSessionStatus string

const (
	ImportSessionStatusUploading  ImportSessionStatus = "uploading"
	ImportSessionStatusReady      ImportSessionStatus = "ready"
	ImportSessionStatusProcessing ImportSessionStatus = "processing"
	ImportSessionStatusCompleted  ImportSessionStatus = "completed"
	ImportSessionStatusFailed     ImportSessionStatus = "failed"
	ImportSessionStatusCancelled  ImportSessionStatus = "cancelled"
)

type ImportSessionErrorCode string

const (
	ImportSessionErrorInvalidRequest ImportSessionErrorCode = "usage_import_session_invalid_request"
	ImportSessionErrorNotFound       ImportSessionErrorCode = "usage_import_session_not_found"
	ImportSessionErrorConflict       ImportSessionErrorCode = "usage_import_session_conflict"
	ImportSessionErrorTooLarge       ImportSessionErrorCode = "usage_import_session_too_large"
	ImportSessionErrorQuotaExceeded  ImportSessionErrorCode = "usage_import_session_quota_exceeded"
	ImportSessionErrorLimitExceeded  ImportSessionErrorCode = "usage_import_session_limit_exceeded"
	ImportSessionErrorUnavailable    ImportSessionErrorCode = "usage_import_session_unavailable"
)

type ImportSessionError struct {
	Code    ImportSessionErrorCode
	Message string
	err     error
}

func (e *ImportSessionError) Error() string {
	if e.err == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.err)
}

func (e *ImportSessionError) Unwrap() error {
	return e.err
}

func newImportSessionError(code ImportSessionErrorCode, message string, err error) error {
	return &ImportSessionError{Code: code, Message: message, err: err}
}

type ImportSessionConfig struct {
	Directory      string
	ChunkSizeBytes int64
	DiskQuotaBytes int64
	MaxSessions    int
	TTL            time.Duration
	Now            func() time.Time
}

type ImportSession struct {
	ID              string              `json:"id"`
	Filename        string              `json:"filename"`
	Status          ImportSessionStatus `json:"status"`
	SizeBytes       int64               `json:"size_bytes"`
	ReceivedBytes   int64               `json:"received_bytes"`
	ChunkSizeBytes  int64               `json:"chunk_size_bytes"`
	CreatedAtMS     int64               `json:"created_at_ms"`
	UpdatedAtMS     int64               `json:"updated_at_ms"`
	ExpiresAtMS     int64               `json:"expires_at_ms"`
	Retryable       bool                `json:"retryable"`
	Error           string              `json:"error,omitempty"`
	Result          *ImportResult       `json:"result,omitempty"`
	resumeKey       string
	cancelRequested bool
}

type importSessionMetadata struct {
	ImportSession
	ResumeKey       string `json:"resume_key,omitempty"`
	CancelRequested bool   `json:"cancel_requested,omitempty"`
}

type importSessionState struct {
	session         ImportSession
	cancel          context.CancelFunc
	cancelRequested bool
	chunkInProgress bool
	chunkCancel     context.CancelFunc
	chunkClose      io.Closer
	chunkDone       chan struct{}
}

type importSessionManager struct {
	config ImportSessionConfig

	mu             sync.Mutex
	sessions       map[string]*importSessionState
	cleanupPending map[string]struct{}
	removeFiles    func(string, bool) error
	initialized    bool
	initErr        error
	started        bool
	rootCtx        context.Context
}

type importSessionImporter func(context.Context, io.Reader) (ImportResult, error)

func newImportSessionManager(config ImportSessionConfig) *importSessionManager {
	if config.Now == nil {
		config.Now = time.Now
	}
	return &importSessionManager{
		config:         config,
		sessions:       make(map[string]*importSessionState),
		cleanupPending: make(map[string]struct{}),
	}
}

func (m *importSessionManager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureInitializedLocked(); err != nil {
		return err
	}
	if m.started {
		return nil
	}
	m.rootCtx = ctx
	m.started = true
	interval := m.config.TTL / 4
	if interval > 15*time.Minute {
		interval = 15 * time.Minute
	}
	if interval < time.Second {
		interval = time.Second
	}
	go m.cleanupLoop(ctx, interval)
	return nil
}

func (m *importSessionManager) cleanupLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := m.CleanupExpired(context.Background()); err != nil {
				log.Printf("cleanup usage import sessions: %v", err)
			}
		}
	}
}

func (m *importSessionManager) Create(
	ctx context.Context,
	filename string,
	sizeBytes int64,
	resumeKey string,
) (ImportSession, error) {
	if err := ctx.Err(); err != nil {
		return ImportSession{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureInitializedLocked(); err != nil {
		return ImportSession{}, err
	}
	m.cleanupExpiredForRequestLocked(m.nowMS())
	if sizeBytes <= 0 {
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorInvalidRequest,
			"usage import file size must be greater than zero",
			nil,
		)
	}
	resumeKey = strings.TrimSpace(resumeKey)
	if resumeKey != "" && !validImportSessionToken(resumeKey) {
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorInvalidRequest,
			"usage import resume key is invalid",
			nil,
		)
	}
	filename = sanitizeImportFilename(filename)
	if resumeKey != "" {
		for _, state := range m.sessions {
			if state.session.resumeKey != resumeKey {
				continue
			}
			if state.session.SizeBytes != sizeBytes || state.session.Filename != filename {
				return ImportSession{}, newImportSessionError(
					ImportSessionErrorConflict,
					"usage import resume key belongs to a different file",
					nil,
				)
			}
			return cloneImportSession(state.session), nil
		}
	}
	if sizeBytes > m.config.DiskQuotaBytes {
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorTooLarge,
			"usage import file exceeds the configured disk quota",
			nil,
		)
	}
	if m.activeSessionCountLocked() >= m.config.MaxSessions {
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorLimitExceeded,
			"usage import active session limit reached",
			nil,
		)
	}
	reservedBytes := m.reservedBytesLocked()
	if reservedBytes >= m.config.DiskQuotaBytes || sizeBytes > m.config.DiskQuotaBytes-reservedBytes {
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorQuotaExceeded,
			"usage import disk quota is fully reserved",
			nil,
		)
	}

	id, err := newImportSessionID()
	if err != nil {
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorUnavailable,
			"generate usage import session id",
			err,
		)
	}
	dataPath, metadataPath, err := m.sessionPaths(id)
	if err != nil {
		return ImportSession{}, err
	}
	file, err := os.OpenFile(dataPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorUnavailable,
			"create usage import temporary file",
			err,
		)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(dataPath)
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorUnavailable,
			"close usage import temporary file",
			err,
		)
	}
	nowMS := m.nowMS()
	session := ImportSession{
		ID:             id,
		Filename:       filename,
		Status:         ImportSessionStatusUploading,
		SizeBytes:      sizeBytes,
		ChunkSizeBytes: minInt64(m.config.ChunkSizeBytes, sizeBytes),
		CreatedAtMS:    nowMS,
		UpdatedAtMS:    nowMS,
		ExpiresAtMS:    nowMS + m.config.TTL.Milliseconds(),
		resumeKey:      resumeKey,
	}
	if err := m.writeMetadataLocked(session); err != nil {
		_ = os.Remove(dataPath)
		_ = os.Remove(metadataPath)
		return ImportSession{}, err
	}
	m.sessions[id] = &importSessionState{session: session}
	return cloneImportSession(session), nil
}

func (m *importSessionManager) Get(ctx context.Context, id string) (ImportSession, error) {
	if err := ctx.Err(); err != nil {
		return ImportSession{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureInitializedLocked(); err != nil {
		return ImportSession{}, err
	}
	m.cleanupExpiredForRequestLocked(m.nowMS())
	state, err := m.findSessionLocked(id)
	if err != nil {
		return ImportSession{}, err
	}
	return cloneImportSession(state.session), nil
}

func (m *importSessionManager) WriteChunk(
	ctx context.Context,
	id string,
	offset int64,
	contentLength int64,
	reader io.Reader,
) (ImportSession, error) {
	if err := ctx.Err(); err != nil {
		return ImportSession{}, err
	}
	m.mu.Lock()
	if err := m.ensureInitializedLocked(); err != nil {
		m.mu.Unlock()
		return ImportSession{}, err
	}
	m.cleanupExpiredForRequestLocked(m.nowMS())
	state, err := m.findSessionLocked(id)
	if err != nil {
		m.mu.Unlock()
		return ImportSession{}, err
	}
	if state.chunkInProgress {
		m.mu.Unlock()
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorConflict,
			"usage import chunk is already in progress",
			nil,
		)
	}
	if state.session.Status != ImportSessionStatusUploading {
		m.mu.Unlock()
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorConflict,
			"usage import session is not accepting chunks",
			nil,
		)
	}
	if offset != state.session.ReceivedBytes {
		m.mu.Unlock()
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorConflict,
			fmt.Sprintf("usage import offset mismatch: expected %d", state.session.ReceivedBytes),
			nil,
		)
	}
	remaining := state.session.SizeBytes - offset
	if remaining <= 0 {
		m.mu.Unlock()
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorConflict,
			"usage import session is already fully uploaded",
			nil,
		)
	}
	maxChunkBytes := minInt64(state.session.ChunkSizeBytes, remaining)
	if contentLength == 0 {
		m.mu.Unlock()
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorInvalidRequest,
			"usage import chunk must not be empty",
			nil,
		)
	}
	if contentLength > maxChunkBytes {
		m.mu.Unlock()
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorTooLarge,
			"usage import chunk exceeds the allowed size",
			nil,
		)
	}

	dataPath, _, err := m.sessionPaths(id)
	if err != nil {
		m.mu.Unlock()
		return ImportSession{}, err
	}
	file, info, err := openValidatedRegularFile(dataPath, os.O_WRONLY)
	if err != nil {
		m.mu.Unlock()
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorUnavailable,
			"validate usage import temporary file",
			err,
		)
	}
	if info.Size() != offset {
		_ = file.Close()
		m.mu.Unlock()
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorConflict,
			fmt.Sprintf("usage import temporary file offset mismatch: expected %d", info.Size()),
			nil,
		)
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		_ = file.Close()
		m.mu.Unlock()
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorUnavailable,
			"seek usage import temporary file",
			err,
		)
	}
	chunkCtx, chunkCancel := context.WithCancel(ctx)
	chunkDone := make(chan struct{})
	state.chunkInProgress = true
	state.chunkCancel = chunkCancel
	if closer, ok := reader.(io.Closer); ok {
		state.chunkClose = closer
	}
	state.chunkDone = chunkDone
	m.mu.Unlock()

	limited := &io.LimitedReader{R: &contextReader{ctx: chunkCtx, reader: reader}, N: maxChunkBytes + 1}
	written, copyErr := io.Copy(file, limited)

	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.finishChunkLocked(state)
	if state.cancelRequested {
		if rollbackErr := rollbackOpenRegularFile(file, offset); rollbackErr != nil {
			log.Printf("rollback cancelled usage import chunk %s: %v", id, rollbackErr)
		}
		next := cloneImportSession(state.session)
		next.Status = ImportSessionStatusCancelled
		next.Retryable = false
		next.Error = "usage import cancelled"
		next.cancelRequested = false
		m.touchSession(&next)
		if err := m.writeMetadataLocked(next); err != nil {
			log.Printf("persist cancelled usage import session %s: %v", id, err)
		}
		state.session = next
		state.cancelRequested = false
		if err := os.Remove(dataPath); err != nil && !os.IsNotExist(err) {
			log.Printf("remove cancelled usage import temporary file %s: %v", id, err)
		}
		return cloneImportSession(next), nil
	}
	if copyErr == nil && written > maxChunkBytes {
		copyErr = newImportSessionError(
			ImportSessionErrorTooLarge,
			"usage import chunk exceeds the allowed size",
			nil,
		)
	}
	if copyErr == nil && contentLength >= 0 && written != contentLength {
		copyErr = newImportSessionError(
			ImportSessionErrorInvalidRequest,
			"usage import chunk length does not match Content-Length",
			nil,
		)
	}
	if copyErr == nil && written == 0 {
		copyErr = newImportSessionError(
			ImportSessionErrorInvalidRequest,
			"usage import chunk must not be empty",
			nil,
		)
	}
	if copyErr != nil {
		if rollbackErr := rollbackOpenRegularFile(file, offset); rollbackErr != nil {
			return ImportSession{}, newImportSessionError(
				ImportSessionErrorUnavailable,
				"rollback usage import chunk",
				errors.Join(copyErr, rollbackErr),
			)
		}
		return ImportSession{}, copyErr
	}
	if err := file.Sync(); err != nil {
		rollbackErr := rollbackOpenRegularFile(file, offset)
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorUnavailable,
			"sync usage import chunk",
			errors.Join(err, rollbackErr),
		)
	}
	if err := file.Close(); err != nil {
		rollbackErr := truncateMatchingRegularFile(dataPath, info, offset)
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorUnavailable,
			"close usage import chunk",
			errors.Join(err, rollbackErr),
		)
	}

	next := cloneImportSession(state.session)
	next.ReceivedBytes += written
	if next.ReceivedBytes == next.SizeBytes {
		next.Status = ImportSessionStatusReady
	}
	m.touchSession(&next)
	if err := m.writeMetadataLocked(next); err != nil {
		if rollbackErr := truncateMatchingRegularFile(dataPath, info, offset); rollbackErr != nil {
			return ImportSession{}, newImportSessionError(
				ImportSessionErrorUnavailable,
				"persist usage import session metadata and rollback chunk",
				errors.Join(err, rollbackErr),
			)
		}
		return ImportSession{}, err
	}
	state.session = next
	return cloneImportSession(next), nil
}

func (m *importSessionManager) Complete(
	ctx context.Context,
	id string,
	importer importSessionImporter,
) (ImportSession, error) {
	if err := ctx.Err(); err != nil {
		return ImportSession{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureInitializedLocked(); err != nil {
		return ImportSession{}, err
	}
	m.cleanupExpiredForRequestLocked(m.nowMS())
	state, err := m.findSessionLocked(id)
	if err != nil {
		return ImportSession{}, err
	}
	if state.chunkInProgress {
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorConflict,
			"usage import chunk is still in progress",
			nil,
		)
	}
	switch state.session.Status {
	case ImportSessionStatusProcessing, ImportSessionStatusCompleted:
		return cloneImportSession(state.session), nil
	case ImportSessionStatusCancelled:
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorConflict,
			"usage import session is cancelled",
			nil,
		)
	case ImportSessionStatusFailed:
		if !state.session.Retryable {
			return ImportSession{}, newImportSessionError(
				ImportSessionErrorConflict,
				"usage import session failed and cannot be retried",
				nil,
			)
		}
	case ImportSessionStatusReady:
		// Continue below.
	default:
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorConflict,
			"usage import upload is not complete",
			nil,
		)
	}
	dataPath, _, err := m.sessionPaths(id)
	if err != nil {
		return ImportSession{}, err
	}
	info, err := validateRegularFile(dataPath)
	if err != nil || info.Size() != state.session.SizeBytes {
		return ImportSession{}, newImportSessionError(
			ImportSessionErrorConflict,
			"usage import temporary file is incomplete",
			err,
		)
	}
	next := cloneImportSession(state.session)
	next.Status = ImportSessionStatusProcessing
	next.Retryable = false
	next.Error = ""
	next.Result = nil
	next.cancelRequested = false
	m.touchSession(&next)
	if err := m.writeMetadataLocked(next); err != nil {
		return ImportSession{}, err
	}
	rootCtx := m.rootCtx
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	processingCtx, cancel := context.WithCancel(rootCtx)
	state.session = next
	state.cancel = cancel
	state.cancelRequested = false
	go m.runImport(id, dataPath, processingCtx, importer)
	return cloneImportSession(next), nil
}

func (m *importSessionManager) Cancel(ctx context.Context, id string) (ImportSession, error) {
	if err := ctx.Err(); err != nil {
		return ImportSession{}, err
	}
	m.mu.Lock()
	if err := m.ensureInitializedLocked(); err != nil {
		m.mu.Unlock()
		return ImportSession{}, err
	}
	m.cleanupExpiredForRequestLocked(m.nowMS())
	state, err := m.findSessionLocked(id)
	if err != nil {
		m.mu.Unlock()
		return ImportSession{}, err
	}
	if state.session.Status == ImportSessionStatusCompleted || state.session.Status == ImportSessionStatusCancelled {
		session := cloneImportSession(state.session)
		m.mu.Unlock()
		return session, nil
	}
	if state.chunkInProgress {
		next := cloneImportSession(state.session)
		next.cancelRequested = true
		m.touchSession(&next)
		if err := m.writeMetadataLocked(next); err != nil {
			m.mu.Unlock()
			return ImportSession{}, err
		}
		state.session = next
		state.cancelRequested = true
		chunkCancel := state.chunkCancel
		chunkClose := state.chunkClose
		chunkDone := state.chunkDone
		if chunkCancel != nil {
			chunkCancel()
		}
		m.mu.Unlock()
		if chunkClose != nil {
			_ = chunkClose.Close()
		}
		select {
		case <-chunkDone:
		case <-ctx.Done():
			return ImportSession{}, ctx.Err()
		}
		m.mu.Lock()
		state, err = m.findSessionLocked(id)
		if err != nil {
			m.mu.Unlock()
			return ImportSession{}, err
		}
		session := cloneImportSession(state.session)
		m.mu.Unlock()
		return session, nil
	}
	if state.cancel != nil {
		next := cloneImportSession(state.session)
		next.cancelRequested = true
		m.touchSession(&next)
		if err := m.writeMetadataLocked(next); err != nil {
			m.mu.Unlock()
			return ImportSession{}, err
		}
		state.session = next
		state.cancelRequested = true
		state.cancel()
		session := cloneImportSession(next)
		m.mu.Unlock()
		return session, nil
	}
	next := cloneImportSession(state.session)
	next.Status = ImportSessionStatusCancelled
	next.Retryable = false
	next.Error = "usage import cancelled"
	next.cancelRequested = false
	m.touchSession(&next)
	if err := m.writeMetadataLocked(next); err != nil {
		m.mu.Unlock()
		return ImportSession{}, err
	}
	state.session = next
	state.cancelRequested = false
	dataPath, _, pathErr := m.sessionPaths(id)
	if pathErr == nil {
		if err := os.Remove(dataPath); err != nil && !os.IsNotExist(err) {
			log.Printf("remove cancelled usage import temporary file %s: %v", id, err)
		}
	}
	session := cloneImportSession(next)
	m.mu.Unlock()
	return session, nil
}

func (m *importSessionManager) CleanupExpired(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureInitializedLocked(); err != nil {
		return 0, err
	}
	return m.cleanupExpiredLocked(m.nowMS())
}

func (m *importSessionManager) runImport(
	id string,
	dataPath string,
	ctx context.Context,
	importer importSessionImporter,
) {
	file, _, openErr := openValidatedRegularFile(dataPath, os.O_RDONLY)
	var result ImportResult
	var importErr error
	if openErr != nil {
		importErr = openErr
	} else {
		result, importErr = importer(ctx, file)
		if closeErr := file.Close(); importErr == nil && closeErr != nil {
			importErr = closeErr
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.sessions[id]
	if !ok {
		return
	}
	state.cancel = nil
	if ctx.Err() != nil && !state.cancelRequested && state.session.Status == ImportSessionStatusProcessing {
		// Server shutdown leaves the durable metadata in processing state. Startup
		// recovery converts it back to ready so already committed batches can be
		// retried safely through the event identity ledger.
		return
	}

	next := cloneImportSession(state.session)
	next.Result = &result
	removeData := false
	if state.cancelRequested || next.Status == ImportSessionStatusCancelled {
		next.Status = ImportSessionStatusCancelled
		next.Retryable = false
		next.Error = "usage import cancelled"
		removeData = true
	} else if importErr != nil {
		var persistenceErr *ImportPersistenceError
		next.Status = ImportSessionStatusFailed
		next.Error = importErr.Error()
		next.Retryable = errors.As(importErr, &persistenceErr) && !errors.Is(importErr, context.Canceled)
		removeData = !next.Retryable
	} else {
		next.Status = ImportSessionStatusCompleted
		next.Retryable = false
		next.Error = ""
		removeData = true
	}
	next.cancelRequested = false
	m.touchSession(&next)
	if err := m.writeMetadataLocked(next); err != nil {
		if next.Status != ImportSessionStatusCancelled {
			fallback := cloneImportSession(next)
			fallback.Status = ImportSessionStatusFailed
			fallback.Retryable = true
			fallback.Error = fmt.Sprintf("persist usage import final state: %v", err)
			state.session = fallback
			state.cancelRequested = false
			log.Printf("persist usage import session %s: %v", id, err)
			return
		}
		state.session = next
		state.cancelRequested = false
		log.Printf("persist usage import session %s: %v", id, err)
	} else {
		state.session = next
		state.cancelRequested = false
	}
	if removeData {
		if err := os.Remove(dataPath); err != nil && !os.IsNotExist(err) {
			log.Printf("remove usage import temporary file %s: %v", id, err)
		}
	}
}

func (m *importSessionManager) ensureInitializedLocked() error {
	if m.initialized {
		return m.initErr
	}
	m.initialized = true
	if strings.TrimSpace(m.config.Directory) == "" || m.config.ChunkSizeBytes <= 0 ||
		m.config.DiskQuotaBytes <= 0 || m.config.MaxSessions <= 0 || m.config.TTL <= 0 {
		m.initErr = newImportSessionError(
			ImportSessionErrorUnavailable,
			"usage import session configuration is invalid",
			nil,
		)
		return m.initErr
	}
	if err := os.MkdirAll(m.config.Directory, 0o700); err != nil {
		m.initErr = newImportSessionError(
			ImportSessionErrorUnavailable,
			"create usage import session directory",
			err,
		)
		return m.initErr
	}
	directoryInfo, err := os.Lstat(m.config.Directory)
	if err != nil || directoryInfo.Mode()&os.ModeSymlink != 0 || !directoryInfo.IsDir() {
		if err == nil {
			err = errors.New("path is not a directory")
		}
		m.initErr = newImportSessionError(
			ImportSessionErrorUnavailable,
			"validate usage import session directory",
			err,
		)
		return m.initErr
	}
	if err := os.Chmod(m.config.Directory, 0o700); err != nil {
		m.initErr = newImportSessionError(
			ImportSessionErrorUnavailable,
			"secure usage import session directory",
			err,
		)
		return m.initErr
	}
	entries, err := os.ReadDir(m.config.Directory)
	if err != nil {
		m.initErr = newImportSessionError(
			ImportSessionErrorUnavailable,
			"read usage import session directory",
			err,
		)
		return m.initErr
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	nowMS := m.nowMS()
	known := make(map[string]struct{})
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if !validImportSessionID(id) {
			continue
		}
		known[id] = struct{}{}
		metadataPath := filepath.Join(m.config.Directory, name)
		info, statErr := os.Lstat(metadataPath)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			m.removeSessionFilesBestEffort(id, true)
			continue
		}
		data, readErr := os.ReadFile(metadataPath)
		var metadata importSessionMetadata
		if readErr != nil || json.Unmarshal(data, &metadata) != nil {
			m.removeSessionFilesBestEffort(id, true)
			continue
		}
		session := metadata.ImportSession
		session.resumeKey = strings.TrimSpace(metadata.ResumeKey)
		session.cancelRequested = metadata.CancelRequested
		if session.ID != id ||
			session.SizeBytes <= 0 || session.ReceivedBytes < 0 || session.ReceivedBytes > session.SizeBytes ||
			!validImportSessionStatus(session.Status) ||
			(session.resumeKey != "" && !validImportSessionToken(session.resumeKey)) {
			m.removeSessionFilesBestEffort(id, true)
			continue
		}
		changed := false
		maxChunkBytes := minInt64(m.config.ChunkSizeBytes, session.SizeBytes)
		if session.ChunkSizeBytes <= 0 || session.ChunkSizeBytes > maxChunkBytes {
			session.ChunkSizeBytes = maxChunkBytes
			changed = true
		}
		if session.ExpiresAtMS > 0 && nowMS >= session.ExpiresAtMS && session.Status != ImportSessionStatusProcessing {
			m.removeSessionFilesBestEffort(id, true)
			continue
		}
		if session.cancelRequested {
			if session.Status != ImportSessionStatusProcessing {
				m.removeSessionFilesBestEffort(id, true)
				continue
			}
			session.Status = ImportSessionStatusCancelled
			session.Retryable = false
			session.Error = "usage import cancelled"
			session.cancelRequested = false
			m.touchSession(&session)
			dataPath, _, pathErr := m.sessionPaths(id)
			if pathErr == nil {
				_ = os.Remove(dataPath)
			}
			changed = true
		} else if sessionReservesDisk(session) {
			dataPath, _, pathErr := m.sessionPaths(id)
			if pathErr != nil {
				m.removeSessionFilesBestEffort(id, true)
				continue
			}
			dataInfo, dataErr := validateRegularFile(dataPath)
			if dataErr != nil || dataInfo.Size() > session.SizeBytes {
				session.Status = ImportSessionStatusFailed
				session.Retryable = false
				session.Error = "usage import temporary file is missing or invalid"
				m.touchSession(&session)
				_ = os.Remove(dataPath)
				changed = true
			} else {
				if session.ReceivedBytes != dataInfo.Size() {
					session.ReceivedBytes = dataInfo.Size()
					changed = true
				}
				if session.Status == ImportSessionStatusProcessing {
					if session.ReceivedBytes == session.SizeBytes {
						session.Status = ImportSessionStatusReady
					} else {
						session.Status = ImportSessionStatusUploading
					}
					session.Retryable = false
					session.Error = ""
					m.touchSession(&session)
					changed = true
				} else if session.ReceivedBytes < session.SizeBytes &&
					(session.Status == ImportSessionStatusReady ||
						(session.Status == ImportSessionStatusFailed && session.Retryable)) {
					session.Status = ImportSessionStatusUploading
					session.Retryable = false
					session.Error = ""
					session.Result = nil
					m.touchSession(&session)
					changed = true
				} else if session.Status == ImportSessionStatusUploading && session.ReceivedBytes == session.SizeBytes {
					session.Status = ImportSessionStatusReady
					m.touchSession(&session)
					changed = true
				}
			}
		} else {
			dataPath, _, pathErr := m.sessionPaths(id)
			if pathErr == nil {
				_ = os.Remove(dataPath)
			}
		}
		m.sessions[id] = &importSessionState{session: session}
		if changed {
			if err := m.writeMetadataLocked(session); err != nil {
				log.Printf("recover usage import session %s: %v", id, err)
			}
		}
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".part") {
			id := strings.TrimSuffix(name, ".part")
			if validImportSessionID(id) {
				if _, ok := known[id]; !ok {
					_ = os.Remove(filepath.Join(m.config.Directory, name))
				}
			}
		}
	}
	return nil
}

func (m *importSessionManager) cleanupExpiredLocked(nowMS int64) (int, error) {
	removed := 0
	var cleanupErr error
	for id := range m.cleanupPending {
		if _, active := m.sessions[id]; active {
			delete(m.cleanupPending, id)
			continue
		}
		if err := m.removeSessionFiles(id, true); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("retry cleanup session %s: %w", id, err))
			continue
		}
		delete(m.cleanupPending, id)
	}
	for id, state := range m.sessions {
		if state.chunkInProgress || state.cancel != nil || state.session.Status == ImportSessionStatusProcessing ||
			state.session.ExpiresAtMS <= 0 || nowMS < state.session.ExpiresAtMS {
			continue
		}
		if err := m.removeSessionFiles(id, true); err != nil {
			m.cleanupPending[id] = struct{}{}
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("cleanup session %s: %w", id, err))
		}
		delete(m.sessions, id)
		removed++
	}
	if cleanupErr != nil {
		return removed, newImportSessionError(
			ImportSessionErrorUnavailable,
			"cleanup expired usage import sessions",
			cleanupErr,
		)
	}
	return removed, nil
}

func (m *importSessionManager) cleanupExpiredForRequestLocked(nowMS int64) {
	if _, err := m.cleanupExpiredLocked(nowMS); err != nil {
		log.Printf("cleanup usage import sessions during request: %v", err)
	}
}

func (m *importSessionManager) finishChunkLocked(state *importSessionState) {
	if state.chunkCancel != nil {
		state.chunkCancel()
	}
	if state.chunkDone != nil {
		close(state.chunkDone)
	}
	state.chunkInProgress = false
	state.chunkCancel = nil
	state.chunkClose = nil
	state.chunkDone = nil
}

func (m *importSessionManager) findSessionLocked(id string) (*importSessionState, error) {
	if !validImportSessionID(id) {
		return nil, newImportSessionError(
			ImportSessionErrorNotFound,
			"usage import session not found",
			nil,
		)
	}
	state, ok := m.sessions[id]
	if !ok {
		return nil, newImportSessionError(
			ImportSessionErrorNotFound,
			"usage import session not found",
			nil,
		)
	}
	return state, nil
}

func (m *importSessionManager) writeMetadataLocked(session ImportSession) error {
	_, metadataPath, err := m.sessionPaths(session.ID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(importSessionMetadata{
		ImportSession:   session,
		ResumeKey:       session.resumeKey,
		CancelRequested: session.cancelRequested,
	})
	if err != nil {
		return newImportSessionError(
			ImportSessionErrorUnavailable,
			"encode usage import session metadata",
			err,
		)
	}
	temporary, err := os.CreateTemp(m.config.Directory, ".usage-import-*.tmp")
	if err != nil {
		return newImportSessionError(
			ImportSessionErrorUnavailable,
			"create usage import session metadata",
			err,
		)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(append(data, '\n'))
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temporaryPath, metadataPath)
	}
	if err != nil {
		return newImportSessionError(
			ImportSessionErrorUnavailable,
			"persist usage import session metadata",
			err,
		)
	}
	return nil
}

func (m *importSessionManager) sessionPaths(id string) (string, string, error) {
	if !validImportSessionID(id) {
		return "", "", newImportSessionError(
			ImportSessionErrorNotFound,
			"usage import session not found",
			nil,
		)
	}
	dataPath := filepath.Join(m.config.Directory, id+".part")
	metadataPath := filepath.Join(m.config.Directory, id+".json")
	for _, path := range []string{dataPath, metadataPath} {
		relative, err := filepath.Rel(m.config.Directory, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return "", "", newImportSessionError(
				ImportSessionErrorInvalidRequest,
				"usage import session path is invalid",
				err,
			)
		}
	}
	return dataPath, metadataPath, nil
}

func (m *importSessionManager) removeSessionFiles(id string, includeMetadata bool) error {
	if m.removeFiles != nil {
		return m.removeFiles(id, includeMetadata)
	}
	dataPath, metadataPath, err := m.sessionPaths(id)
	if err != nil {
		return err
	}
	var removeErr error
	if err := os.Remove(dataPath); err != nil && !os.IsNotExist(err) {
		removeErr = errors.Join(removeErr, err)
	}
	if includeMetadata {
		if err := os.Remove(metadataPath); err != nil && !os.IsNotExist(err) {
			removeErr = errors.Join(removeErr, err)
		}
	}
	return removeErr
}

func (m *importSessionManager) removeSessionFilesBestEffort(id string, includeMetadata bool) {
	if err := m.removeSessionFiles(id, includeMetadata); err != nil && includeMetadata {
		m.cleanupPending[id] = struct{}{}
	}
}

func (m *importSessionManager) activeSessionCountLocked() int {
	count := 0
	for _, state := range m.sessions {
		if state.chunkInProgress || state.cancel != nil || state.cancelRequested || sessionReservesDisk(state.session) {
			count++
		}
	}
	return count
}

func (m *importSessionManager) reservedBytesLocked() int64 {
	var total int64
	for _, state := range m.sessions {
		if state.chunkInProgress || state.cancel != nil || state.cancelRequested || sessionReservesDisk(state.session) {
			if total >= m.config.DiskQuotaBytes || state.session.SizeBytes > m.config.DiskQuotaBytes-total {
				return m.config.DiskQuotaBytes
			}
			total += state.session.SizeBytes
		}
	}
	return total
}

func (m *importSessionManager) touchSession(session *ImportSession) {
	nowMS := m.nowMS()
	session.UpdatedAtMS = nowMS
	session.ExpiresAtMS = nowMS + m.config.TTL.Milliseconds()
}

func (m *importSessionManager) nowMS() int64 {
	return m.config.Now().UnixMilli()
}

func sessionReservesDisk(session ImportSession) bool {
	switch session.Status {
	case ImportSessionStatusUploading, ImportSessionStatusReady, ImportSessionStatusProcessing:
		return true
	case ImportSessionStatusFailed:
		return session.Retryable
	default:
		return false
	}
}

func cloneImportSession(session ImportSession) ImportSession {
	clone := session
	if session.Result != nil {
		result := *session.Result
		result.Warnings = append([]string(nil), session.Result.Warnings...)
		clone.Result = &result
	}
	return clone
}

func newImportSessionID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return hex.EncodeToString(random), nil
}

func validImportSessionID(value string) bool {
	return validImportSessionToken(value)
}

func validImportSessionToken(value string) bool {
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

func validImportSessionStatus(status ImportSessionStatus) bool {
	switch status {
	case ImportSessionStatusUploading,
		ImportSessionStatusReady,
		ImportSessionStatusProcessing,
		ImportSessionStatusCompleted,
		ImportSessionStatusFailed,
		ImportSessionStatusCancelled:
		return true
	default:
		return false
	}
}

func sanitizeImportFilename(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\x00", "")
	value = filepath.Base(value)
	if value == "" || value == "." || value == string(filepath.Separator) {
		value = "usage-import.jsonl"
	}
	runes := []rune(value)
	if len(runes) > 240 {
		value = string(runes[:240])
	}
	return value
}

func validateRegularFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("path is not a regular file")
	}
	return info, nil
}

func openValidatedRegularFile(path string, flag int) (*os.File, os.FileInfo, error) {
	expected, err := validateRegularFile(path)
	if err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(path, flag, 0)
	if err != nil {
		return nil, nil, err
	}
	actual, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !actual.Mode().IsRegular() || !os.SameFile(expected, actual) {
		_ = file.Close()
		return nil, nil, errors.New("path changed while opening regular file")
	}
	return file, actual, nil
}

func truncateMatchingRegularFile(path string, expected os.FileInfo, size int64) error {
	file, actual, err := openValidatedRegularFile(path, os.O_WRONLY)
	if err != nil {
		return err
	}
	if !os.SameFile(expected, actual) {
		_ = file.Close()
		return errors.New("path changed before truncating regular file")
	}
	if err := file.Truncate(size); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func rollbackOpenRegularFile(file *os.File, size int64) error {
	var rollbackErr error
	if err := file.Truncate(size); err != nil {
		rollbackErr = errors.Join(rollbackErr, err)
	} else if err := file.Sync(); err != nil {
		rollbackErr = errors.Join(rollbackErr, err)
	}
	if err := file.Close(); err != nil {
		rollbackErr = errors.Join(rollbackErr, err)
	}
	return rollbackErr
}

func minInt64(left int64, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(buffer)
	}
}

type contextReadSeeker struct {
	ctx    context.Context
	reader io.ReadSeeker
}

func (r *contextReadSeeker) Read(buffer []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(buffer)
	}
}

func (r *contextReadSeeker) Seek(offset int64, whence int) (int64, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Seek(offset, whence)
}
