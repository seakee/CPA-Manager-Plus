package usage

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	usageparser "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

type ImportResult struct {
	Format      string   `json:"format"`
	Added       int      `json:"added"`
	Skipped     int      `json:"skipped"`
	Total       int      `json:"total"`
	Failed      int      `json:"failed"`
	Unsupported int      `json:"unsupported"`
	Warnings    []string `json:"warnings"`
}

type ImportPersistenceError struct {
	err error
}

func (e *ImportPersistenceError) Error() string {
	return fmt.Sprintf("persist usage import batch: %v", e.err)
}

func (e *ImportPersistenceError) Unwrap() error {
	return e.err
}

type Service struct {
	store                  *store.Store
	notifierMu             sync.RWMutex
	eventsInsertedNotifier func()
	importSessions         *importSessionManager
}

const importBatchSize = 256

type Option func(*Service)

func WithImportSessions(config ImportSessionConfig) Option {
	return func(service *Service) {
		service.importSessions = newImportSessionManager(config)
	}
}

func New(store *store.Store, options ...Option) *Service {
	service := &Service{store: store}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *Service) SetEventsInsertedNotifier(notifier func()) {
	s.notifierMu.Lock()
	s.eventsInsertedNotifier = notifier
	s.notifierMu.Unlock()
}

func (s *Service) notifyEventsInserted() {
	s.notifierMu.RLock()
	notifier := s.eventsInsertedNotifier
	s.notifierMu.RUnlock()
	if notifier != nil {
		notifier()
	}
}

func (s *Service) WriteCompatibleUsage(ctx context.Context, writer io.Writer, limit int) error {
	return s.store.WriteCompatibleUsage(ctx, writer, limit)
}

func (s *Service) WriteExport(ctx context.Context, writer io.Writer, limit int) error {
	return s.store.WriteExportJSONL(ctx, writer, limit)
}

func (s *Service) Import(ctx context.Context, reader io.Reader) (ImportResult, *usageparser.ImportStreamResult, error) {
	var added int
	var skipped int
	var contextualReader io.Reader = &contextReader{ctx: ctx, reader: reader}
	if seeker, ok := reader.(io.ReadSeeker); ok {
		contextualReader = &contextReadSeeker{ctx: ctx, reader: seeker}
	}
	parsed, err := usageparser.StreamImportPayload(contextualReader, importBatchSize, func(events []usageparser.Event) error {
		result, err := s.store.InsertEvents(ctx, events)
		if err != nil {
			return &ImportPersistenceError{err: err}
		}
		added += result.Inserted
		skipped += result.Skipped
		return nil
	})
	if added > 0 {
		s.notifyEventsInserted()
	}
	result := ImportResult{
		Format:      parsed.Format,
		Added:       added,
		Skipped:     skipped,
		Total:       parsed.Total,
		Failed:      parsed.Failed,
		Unsupported: parsed.Unsupported,
		Warnings:    parsed.Warnings,
	}
	if err != nil {
		return result, &parsed, err
	}
	return result, &parsed, nil
}

func (s *Service) Counts(ctx context.Context) (events int64, deadLetters int64, err error) {
	return s.store.Counts(ctx)
}

func (s *Service) StartImportSessionCleanup(ctx context.Context) error {
	manager, err := s.requireImportSessionManager()
	if err != nil {
		return err
	}
	return manager.Start(ctx)
}

func (s *Service) CreateImportSession(
	ctx context.Context,
	filename string,
	sizeBytes int64,
	resumeKey string,
) (ImportSession, error) {
	manager, err := s.requireImportSessionManager()
	if err != nil {
		return ImportSession{}, err
	}
	return manager.Create(ctx, filename, sizeBytes, resumeKey)
}

func (s *Service) GetImportSession(ctx context.Context, id string) (ImportSession, error) {
	manager, err := s.requireImportSessionManager()
	if err != nil {
		return ImportSession{}, err
	}
	return manager.Get(ctx, id)
}

func (s *Service) WriteImportSessionChunk(
	ctx context.Context,
	id string,
	offset int64,
	contentLength int64,
	reader io.Reader,
) (ImportSession, error) {
	manager, err := s.requireImportSessionManager()
	if err != nil {
		return ImportSession{}, err
	}
	return manager.WriteChunk(ctx, id, offset, contentLength, reader)
}

func (s *Service) CompleteImportSession(ctx context.Context, id string) (ImportSession, error) {
	manager, err := s.requireImportSessionManager()
	if err != nil {
		return ImportSession{}, err
	}
	return manager.Complete(ctx, id, func(importCtx context.Context, reader io.Reader) (ImportResult, error) {
		result, _, importErr := s.Import(importCtx, reader)
		return result, importErr
	})
}

func (s *Service) CancelImportSession(ctx context.Context, id string) (ImportSession, error) {
	manager, err := s.requireImportSessionManager()
	if err != nil {
		return ImportSession{}, err
	}
	return manager.Cancel(ctx, id)
}

func (s *Service) CleanupExpiredImportSessions(ctx context.Context) (int, error) {
	manager, err := s.requireImportSessionManager()
	if err != nil {
		return 0, err
	}
	return manager.CleanupExpired(ctx)
}

func (s *Service) requireImportSessionManager() (*importSessionManager, error) {
	if s.importSessions == nil {
		return nil, newImportSessionError(
			ImportSessionErrorUnavailable,
			"usage import sessions are not configured",
			nil,
		)
	}
	return s.importSessions, nil
}
