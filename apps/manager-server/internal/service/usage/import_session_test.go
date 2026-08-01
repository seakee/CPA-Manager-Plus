package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

func TestImportSessionUploadsChunksAndCompletesWithStreamingParser(t *testing.T) {
	service, cancel := newImportSessionTestService(t, ImportSessionConfig{
		Directory:      filepath.Join(t.TempDir(), "imports"),
		ChunkSizeBytes: 64,
		DiskQuotaBytes: 1024 * 1024,
		MaxSessions:    2,
		TTL:            time.Hour,
	})
	defer cancel()
	payload := strings.Join([]string{
		`{"event_hash":"session-one","timestamp_ms":1,"timestamp":"2026-01-01T00:00:00Z","model":"gpt-test"}`,
		`{"event_hash":"session-two","timestamp_ms":2,"timestamp":"2026-01-01T00:00:01Z","model":"gpt-test"}`,
	}, "\n") + "\n"

	session, err := service.CreateImportSession(context.Background(), "../history.jsonl", int64(len(payload)), "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if session.Filename != "history.jsonl" || session.Status != ImportSessionStatusUploading {
		t.Fatalf("created session = %#v", session)
	}
	for offset := int64(0); offset < int64(len(payload)); {
		end := minInt64(offset+session.ChunkSizeBytes, int64(len(payload)))
		chunk := payload[offset:end]
		session, err = service.WriteImportSessionChunk(
			context.Background(),
			session.ID,
			offset,
			int64(len(chunk)),
			strings.NewReader(chunk),
		)
		if err != nil {
			t.Fatalf("write chunk at %d: %v", offset, err)
		}
		offset = end
	}
	if session.Status != ImportSessionStatusReady || session.ReceivedBytes != session.SizeBytes {
		t.Fatalf("uploaded session = %#v", session)
	}

	session, err = service.CompleteImportSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("complete session: %v", err)
	}
	if session.Status != ImportSessionStatusProcessing {
		t.Fatalf("completion response = %#v", session)
	}
	completed := waitForImportSessionStatus(t, service, session.ID, ImportSessionStatusCompleted)
	if completed.Result == nil || completed.Result.Added != 2 || completed.Result.Total != 2 {
		t.Fatalf("completed session = %#v", completed)
	}
	events, _, err := service.Counts(context.Background())
	if err != nil || events != 2 {
		t.Fatalf("events = %d error = %v", events, err)
	}
	dataPath := filepath.Join(service.importSessions.config.Directory, session.ID+".part")
	if _, err := os.Stat(dataPath); !os.IsNotExist(err) {
		t.Fatalf("completed data file still exists: %v", err)
	}
}

func TestImportHonorsCancelledContextBeforeParsing(t *testing.T) {
	cfg := testutil.NewConfig(t)
	service := New(testutil.NewStore(t, cfg))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := service.Import(ctx, strings.NewReader("not-json\n"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("import error = %v, want context canceled", err)
	}
}

func TestImportSessionRejectsOffsetMismatchAndRollsBackOversizedChunk(t *testing.T) {
	manager := newImportSessionManager(ImportSessionConfig{
		Directory:      filepath.Join(t.TempDir(), "imports"),
		ChunkSizeBytes: 4,
		DiskQuotaBytes: 32,
		MaxSessions:    2,
		TTL:            time.Hour,
	})
	session, err := manager.Create(context.Background(), "usage.jsonl", 8, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = manager.WriteChunk(context.Background(), session.ID, 0, -1, strings.NewReader("12345"))
	requireImportSessionErrorCode(t, err, ImportSessionErrorTooLarge)
	dataPath := filepath.Join(manager.config.Directory, session.ID+".part")
	if info, statErr := os.Stat(dataPath); statErr != nil || info.Size() != 0 {
		t.Fatalf("rolled back size = %v error = %v", info, statErr)
	}
	session, err = manager.WriteChunk(context.Background(), session.ID, 0, 4, strings.NewReader("1234"))
	if err != nil || session.ReceivedBytes != 4 {
		t.Fatalf("valid chunk session = %#v error = %v", session, err)
	}
	_, err = manager.WriteChunk(context.Background(), session.ID, 0, 4, strings.NewReader("5678"))
	requireImportSessionErrorCode(t, err, ImportSessionErrorConflict)
	if info, statErr := os.Stat(dataPath); statErr != nil || info.Size() != 4 {
		t.Fatalf("conflict changed file size = %v error = %v", info, statErr)
	}
}

func TestImportSessionEnforcesReservationQuotaAndActiveLimit(t *testing.T) {
	manager := newImportSessionManager(ImportSessionConfig{
		Directory:      filepath.Join(t.TempDir(), "imports"),
		ChunkSizeBytes: 4,
		DiskQuotaBytes: 20,
		MaxSessions:    1,
		TTL:            time.Hour,
	})
	first, err := manager.Create(context.Background(), "first.jsonl", 10, "")
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	_, err = manager.Create(context.Background(), "second.jsonl", 5, "")
	requireImportSessionErrorCode(t, err, ImportSessionErrorLimitExceeded)
	if _, err := manager.Cancel(context.Background(), first.ID); err != nil {
		t.Fatalf("cancel first: %v", err)
	}
	if _, err := manager.Create(context.Background(), "replacement.jsonl", 20, ""); err != nil {
		t.Fatalf("reservation was not released: %v", err)
	}

	quotaManager := newImportSessionManager(ImportSessionConfig{
		Directory:      filepath.Join(t.TempDir(), "quota-imports"),
		ChunkSizeBytes: 4,
		DiskQuotaBytes: 20,
		MaxSessions:    2,
		TTL:            time.Hour,
	})
	if _, err := quotaManager.Create(context.Background(), "first.jsonl", 12, ""); err != nil {
		t.Fatalf("create quota first: %v", err)
	}
	_, err = quotaManager.Create(context.Background(), "second.jsonl", 9, "")
	requireImportSessionErrorCode(t, err, ImportSessionErrorQuotaExceeded)
}

func TestImportSessionCreateIsIdempotentAcrossLostResponsesAndRestart(t *testing.T) {
	const resumeKey = "0123456789abcdef0123456789abcdef"
	directory := filepath.Join(t.TempDir(), "imports")
	config := ImportSessionConfig{
		Directory:      directory,
		ChunkSizeBytes: 4,
		DiskQuotaBytes: 16,
		MaxSessions:    1,
		TTL:            time.Hour,
	}
	manager := newImportSessionManager(config)
	first, err := manager.Create(context.Background(), "history.jsonl", 8, resumeKey)
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	duplicate, err := manager.Create(context.Background(), "history.jsonl", 8, resumeKey)
	if err != nil || duplicate.ID != first.ID {
		t.Fatalf("duplicate session = %#v error = %v", duplicate, err)
	}
	_, err = manager.Create(context.Background(), "different.jsonl", 8, resumeKey)
	requireImportSessionErrorCode(t, err, ImportSessionErrorConflict)
	_, err = manager.Create(context.Background(), "history.jsonl", 8, "invalid")
	requireImportSessionErrorCode(t, err, ImportSessionErrorInvalidRequest)

	apiPayload, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal API session: %v", err)
	}
	if bytes.Contains(apiPayload, []byte("resume_key")) || bytes.Contains(apiPayload, []byte("cancel_requested")) {
		t.Fatalf("private metadata leaked in API payload: %s", apiPayload)
	}
	if !bytes.Contains(apiPayload, []byte(`"retryable":false`)) {
		t.Fatalf("retryable=false missing from API payload: %s", apiPayload)
	}
	metadata, err := os.ReadFile(filepath.Join(directory, first.ID+".json"))
	if err != nil || !bytes.Contains(metadata, []byte(`"resume_key":"`+resumeKey+`"`)) {
		t.Fatalf("metadata = %s error = %v", metadata, err)
	}

	restarted := newImportSessionManager(config)
	recovered, err := restarted.Create(context.Background(), "history.jsonl", 8, resumeKey)
	if err != nil || recovered.ID != first.ID {
		t.Fatalf("recovered session = %#v error = %v", recovered, err)
	}
}

func TestImportSessionAllowsDeclaredFilesLargerThanLegacyRequestLimit(t *testing.T) {
	const legacyLimit = int64(64 * 1024 * 1024)
	manager := newImportSessionManager(ImportSessionConfig{
		Directory:      filepath.Join(t.TempDir(), "imports"),
		ChunkSizeBytes: 4 * 1024 * 1024,
		DiskQuotaBytes: legacyLimit + 2,
		MaxSessions:    1,
		TTL:            time.Hour,
	})
	session, err := manager.Create(context.Background(), "large.jsonl", legacyLimit+1, "")
	if err != nil {
		t.Fatalf("create large session: %v", err)
	}
	if session.SizeBytes != legacyLimit+1 {
		t.Fatalf("size = %d", session.SizeBytes)
	}
}

func TestImportSessionStreamsFilesLargerThanLegacyRequestLimit(t *testing.T) {
	const legacyLimit = int64(64 * 1024 * 1024)
	const chunkSize = int64(4 * 1024 * 1024)
	manager := newImportSessionManager(ImportSessionConfig{
		Directory:      filepath.Join(t.TempDir(), "imports"),
		ChunkSizeBytes: chunkSize,
		DiskQuotaBytes: legacyLimit + chunkSize,
		MaxSessions:    1,
		TTL:            time.Hour,
	})
	session, err := manager.Create(context.Background(), "large.jsonl", legacyLimit+1, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	chunk := bytes.Repeat([]byte{'x'}, int(chunkSize))
	for offset := int64(0); offset < session.SizeBytes; {
		length := minInt64(chunkSize, session.SizeBytes-offset)
		session, err = manager.WriteChunk(
			context.Background(),
			session.ID,
			offset,
			length,
			bytes.NewReader(chunk[:int(length)]),
		)
		if err != nil {
			t.Fatalf("write chunk at %d: %v", offset, err)
		}
		offset += length
	}
	if session.Status != ImportSessionStatusReady {
		t.Fatalf("uploaded session = %#v", session)
	}
	_, err = manager.Complete(context.Background(), session.ID, func(_ context.Context, reader io.Reader) (ImportResult, error) {
		read, readErr := io.Copy(io.Discard, reader)
		return ImportResult{Total: int(read)}, readErr
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	completed := waitForManagerSessionStatus(t, manager, session.ID, ImportSessionStatusCompleted)
	if completed.Result == nil || int64(completed.Result.Total) != legacyLimit+1 {
		t.Fatalf("completed session = %#v", completed)
	}
}

func TestImportSessionCleanupRemovesExpiredFilesAndMetadata(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	manager := newImportSessionManager(ImportSessionConfig{
		Directory:      filepath.Join(t.TempDir(), "imports"),
		ChunkSizeBytes: 4,
		DiskQuotaBytes: 16,
		MaxSessions:    1,
		TTL:            time.Hour,
		Now:            func() time.Time { return now },
	})
	session, err := manager.Create(context.Background(), "usage.jsonl", 8, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	now = now.Add(2 * time.Hour)
	removed, err := manager.CleanupExpired(context.Background())
	if err != nil || removed != 1 {
		t.Fatalf("cleanup removed = %d error = %v", removed, err)
	}
	_, err = manager.Get(context.Background(), session.ID)
	requireImportSessionErrorCode(t, err, ImportSessionErrorNotFound)
	for _, suffix := range []string{".part", ".json"} {
		if _, statErr := os.Stat(filepath.Join(manager.config.Directory, session.ID+suffix)); !os.IsNotExist(statErr) {
			t.Fatalf("expired %s still exists: %v", suffix, statErr)
		}
	}
}

func TestImportSessionCannotBeResurrectedAfterTTL(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	manager := newImportSessionManager(ImportSessionConfig{
		Directory:      filepath.Join(t.TempDir(), "imports"),
		ChunkSizeBytes: 4,
		DiskQuotaBytes: 16,
		MaxSessions:    1,
		TTL:            time.Hour,
		Now:            func() time.Time { return now },
	})
	session, err := manager.Create(context.Background(), "usage.jsonl", 8, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	now = now.Add(2 * time.Hour)

	_, err = manager.WriteChunk(context.Background(), session.ID, 0, 4, strings.NewReader("data"))
	requireImportSessionErrorCode(t, err, ImportSessionErrorNotFound)
	for _, suffix := range []string{".part", ".json"} {
		if _, statErr := os.Stat(filepath.Join(manager.config.Directory, session.ID+suffix)); !os.IsNotExist(statErr) {
			t.Fatalf("expired %s still exists: %v", suffix, statErr)
		}
	}
}

func TestImportSessionRestartRestoresProcessingMetadataFromActualFileSize(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "imports")
	config := ImportSessionConfig{
		Directory:      directory,
		ChunkSizeBytes: 8,
		DiskQuotaBytes: 32,
		MaxSessions:    2,
		TTL:            time.Hour,
	}
	manager := newImportSessionManager(config)
	session, err := manager.Create(context.Background(), "usage.jsonl", 8, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	session, err = manager.WriteChunk(context.Background(), session.ID, 0, 8, strings.NewReader("12345678"))
	if err != nil || session.Status != ImportSessionStatusReady {
		t.Fatalf("upload = %#v error = %v", session, err)
	}
	metadataPath := filepath.Join(directory, session.ID+".json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var metadata ImportSession
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	metadata.Status = ImportSessionStatusProcessing
	metadata.ReceivedBytes = 0
	data, err = json.Marshal(metadata)
	if err != nil {
		t.Fatalf("encode metadata: %v", err)
	}
	if err := os.WriteFile(metadataPath, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	restarted := newImportSessionManager(config)
	recovered, err := restarted.Get(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered.Status != ImportSessionStatusReady || recovered.ReceivedBytes != recovered.SizeBytes {
		t.Fatalf("recovered session = %#v", recovered)
	}
}

func TestImportSessionRestartRestoresTruncatedReadySessionToUploading(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "imports")
	config := ImportSessionConfig{
		Directory:      directory,
		ChunkSizeBytes: 8,
		DiskQuotaBytes: 32,
		MaxSessions:    2,
		TTL:            time.Hour,
	}
	manager := newImportSessionManager(config)
	session, err := manager.Create(context.Background(), "usage.jsonl", 8, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	session, err = manager.WriteChunk(context.Background(), session.ID, 0, 8, strings.NewReader("12345678"))
	if err != nil || session.Status != ImportSessionStatusReady {
		t.Fatalf("upload = %#v error = %v", session, err)
	}
	dataPath := filepath.Join(directory, session.ID+".part")
	if err := os.Truncate(dataPath, 4); err != nil {
		t.Fatalf("truncate part: %v", err)
	}

	restarted := newImportSessionManager(config)
	recovered, err := restarted.Get(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered.Status != ImportSessionStatusUploading || recovered.ReceivedBytes != 4 {
		t.Fatalf("recovered session = %#v", recovered)
	}
	recovered, err = restarted.WriteChunk(context.Background(), session.ID, 4, 4, strings.NewReader("5678"))
	if err != nil || recovered.Status != ImportSessionStatusReady {
		t.Fatalf("resume = %#v error = %v", recovered, err)
	}
}

func TestImportSessionRestartHonorsPersistedCancellationRequest(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "imports")
	config := ImportSessionConfig{
		Directory:      directory,
		ChunkSizeBytes: 8,
		DiskQuotaBytes: 32,
		MaxSessions:    2,
		TTL:            time.Hour,
	}
	manager := newImportSessionManager(config)
	session, err := manager.Create(context.Background(), "usage.jsonl", 8, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	session, err = manager.WriteChunk(context.Background(), session.ID, 0, 8, strings.NewReader("12345678"))
	if err != nil || session.Status != ImportSessionStatusReady {
		t.Fatalf("upload = %#v error = %v", session, err)
	}
	metadataPath := filepath.Join(directory, session.ID+".json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var metadata importSessionMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	metadata.Status = ImportSessionStatusProcessing
	metadata.CancelRequested = true
	data, err = json.Marshal(metadata)
	if err != nil {
		t.Fatalf("encode metadata: %v", err)
	}
	if err := os.WriteFile(metadataPath, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	restarted := newImportSessionManager(config)
	recovered, err := restarted.Get(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered.Status != ImportSessionStatusCancelled || recovered.Result != nil {
		t.Fatalf("recovered session = %#v", recovered)
	}
	if _, err := os.Stat(filepath.Join(directory, session.ID+".part")); !os.IsNotExist(err) {
		t.Fatalf("cancelled data file still exists: %v", err)
	}
}

func TestImportSessionRejectsSymlinkTemporaryFile(t *testing.T) {
	manager := newImportSessionManager(ImportSessionConfig{
		Directory:      filepath.Join(t.TempDir(), "imports"),
		ChunkSizeBytes: 4,
		DiskQuotaBytes: 16,
		MaxSessions:    1,
		TTL:            time.Hour,
	})
	session, err := manager.Create(context.Background(), "usage.jsonl", 4, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	dataPath := filepath.Join(manager.config.Directory, session.ID+".part")
	targetPath := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(targetPath, []byte("safe"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Remove(dataPath); err != nil {
		t.Fatalf("remove part: %v", err)
	}
	if err := os.Symlink(targetPath, dataPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	_, err = manager.WriteChunk(context.Background(), session.ID, 0, 4, strings.NewReader("evil"))
	requireImportSessionErrorCode(t, err, ImportSessionErrorUnavailable)
	data, err := os.ReadFile(targetPath)
	if err != nil || string(data) != "safe" {
		t.Fatalf("target = %q error = %v", data, err)
	}
}

func TestImportSessionRejectsSymlinkDirectoryAndInvalidID(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("create target: %v", err)
	}
	directory := filepath.Join(root, "imports")
	if err := os.Symlink(target, directory); err != nil {
		t.Fatalf("create directory symlink: %v", err)
	}
	manager := newImportSessionManager(ImportSessionConfig{
		Directory:      directory,
		ChunkSizeBytes: 4,
		DiskQuotaBytes: 16,
		MaxSessions:    1,
		TTL:            time.Hour,
	})
	_, err := manager.Create(context.Background(), "usage.jsonl", 4, "")
	requireImportSessionErrorCode(t, err, ImportSessionErrorUnavailable)
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("symlink target entries = %v error = %v", entries, err)
	}

	safe := newImportSessionManager(ImportSessionConfig{
		Directory:      filepath.Join(t.TempDir(), "safe-imports"),
		ChunkSizeBytes: 4,
		DiskQuotaBytes: 16,
		MaxSessions:    1,
		TTL:            time.Hour,
	})
	_, err = safe.Get(context.Background(), "../not-a-session")
	requireImportSessionErrorCode(t, err, ImportSessionErrorNotFound)
}

func TestImportSessionRetryableFailurePreservesPartialResultAndFile(t *testing.T) {
	manager := newImportSessionManager(ImportSessionConfig{
		Directory:      filepath.Join(t.TempDir(), "imports"),
		ChunkSizeBytes: 4,
		DiskQuotaBytes: 16,
		MaxSessions:    1,
		TTL:            time.Hour,
	})
	session, err := manager.Create(context.Background(), "usage.jsonl", 4, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := manager.WriteChunk(context.Background(), session.ID, 0, 4, strings.NewReader("data")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err = manager.Complete(context.Background(), session.ID, func(context.Context, io.Reader) (ImportResult, error) {
		return ImportResult{Added: 1, Total: 1}, &ImportPersistenceError{err: errors.New("disk busy")}
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	failed := waitForManagerSessionStatus(t, manager, session.ID, ImportSessionStatusFailed)
	if !failed.Retryable || failed.Result == nil || failed.Result.Added != 1 {
		t.Fatalf("failed session = %#v", failed)
	}
	dataPath := filepath.Join(manager.config.Directory, session.ID+".part")
	if info, statErr := os.Stat(dataPath); statErr != nil || info.Size() != 4 {
		t.Fatalf("retry file = %v error = %v", info, statErr)
	}

	_, err = manager.Complete(context.Background(), session.ID, func(context.Context, io.Reader) (ImportResult, error) {
		return ImportResult{Skipped: 1, Total: 1}, nil
	})
	if err != nil {
		t.Fatalf("retry complete: %v", err)
	}
	completed := waitForManagerSessionStatus(t, manager, session.ID, ImportSessionStatusCompleted)
	if completed.Result == nil || completed.Result.Skipped != 1 {
		t.Fatalf("completed session = %#v", completed)
	}
	if _, err := os.Stat(dataPath); !os.IsNotExist(err) {
		t.Fatalf("completed data file still exists: %v", err)
	}
}

func TestImportSessionCancelDuringProcessingKeepsPartialResult(t *testing.T) {
	manager := newImportSessionManager(ImportSessionConfig{
		Directory:      filepath.Join(t.TempDir(), "imports"),
		ChunkSizeBytes: 4,
		DiskQuotaBytes: 16,
		MaxSessions:    1,
		TTL:            time.Hour,
	})
	rootCtx, stop := context.WithCancel(context.Background())
	defer stop()
	if err := manager.Start(rootCtx); err != nil {
		t.Fatalf("start: %v", err)
	}
	session, err := manager.Create(context.Background(), "usage.jsonl", 4, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := manager.WriteChunk(context.Background(), session.ID, 0, 4, strings.NewReader("data")); err != nil {
		t.Fatalf("write: %v", err)
	}
	started := make(chan struct{})
	_, err = manager.Complete(context.Background(), session.ID, func(ctx context.Context, _ io.Reader) (ImportResult, error) {
		close(started)
		<-ctx.Done()
		return ImportResult{Added: 1, Total: 1}, ctx.Err()
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	<-started
	cancelling, err := manager.Cancel(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelling.Status != ImportSessionStatusProcessing || cancelling.Result != nil {
		t.Fatalf("cancelling session = %#v", cancelling)
	}
	cancelled := waitForManagerSessionStatus(t, manager, session.ID, ImportSessionStatusCancelled)
	if cancelled.Result == nil || cancelled.Result.Added != 1 {
		t.Fatalf("cancelled session = %#v", cancelled)
	}
	if _, err := os.Stat(filepath.Join(manager.config.Directory, session.ID+".part")); !os.IsNotExist(err) {
		t.Fatalf("cancelled data file still exists: %v", err)
	}
}

func TestImportSessionChunkUploadDoesNotBlockOtherSessionsAndCanBeCancelled(t *testing.T) {
	manager := newImportSessionManager(ImportSessionConfig{
		Directory:      filepath.Join(t.TempDir(), "imports"),
		ChunkSizeBytes: 4,
		DiskQuotaBytes: 16,
		MaxSessions:    2,
		TTL:            time.Hour,
	})
	uploading, err := manager.Create(context.Background(), "uploading.jsonl", 4, "")
	if err != nil {
		t.Fatalf("create uploading session: %v", err)
	}
	other, err := manager.Create(context.Background(), "other.jsonl", 4, "")
	if err != nil {
		t.Fatalf("create other session: %v", err)
	}

	reader := newBlockingChunkReader()
	type writeResult struct {
		session ImportSession
		err     error
	}
	writeDone := make(chan writeResult, 1)
	go func() {
		session, writeErr := manager.WriteChunk(context.Background(), uploading.ID, 0, 4, reader)
		writeDone <- writeResult{session: session, err: writeErr}
	}()
	select {
	case <-reader.started:
	case <-time.After(time.Second):
		t.Fatal("chunk upload did not start")
	}

	getDone := make(chan error, 1)
	go func() {
		_, getErr := manager.Get(context.Background(), other.ID)
		getDone <- getErr
	}()
	select {
	case getErr := <-getDone:
		if getErr != nil {
			t.Fatalf("get other session: %v", getErr)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("chunk upload blocked access to another session")
	}

	_, err = manager.WriteChunk(context.Background(), uploading.ID, 0, 4, strings.NewReader("data"))
	requireImportSessionErrorCode(t, err, ImportSessionErrorConflict)

	cancelCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cancelled, err := manager.Cancel(cancelCtx, uploading.ID)
	if err != nil {
		t.Fatalf("cancel uploading session: %v", err)
	}
	if cancelled.Status != ImportSessionStatusCancelled {
		t.Fatalf("cancelled session = %#v", cancelled)
	}
	select {
	case result := <-writeDone:
		if result.err != nil || result.session.Status != ImportSessionStatusCancelled {
			t.Fatalf("chunk result = %#v error = %v", result.session, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled chunk upload did not finish")
	}
	if _, err := os.Stat(filepath.Join(manager.config.Directory, uploading.ID+".part")); !os.IsNotExist(err) {
		t.Fatalf("cancelled data file still exists: %v", err)
	}
	manager.mu.Lock()
	state := manager.sessions[uploading.ID]
	if state.chunkInProgress || state.chunkCancel != nil || state.chunkClose != nil || state.chunkDone != nil {
		manager.mu.Unlock()
		t.Fatalf("chunk state was not released: %#v", state)
	}
	manager.mu.Unlock()
}

func TestImportSessionCleanupFailureDoesNotBreakRequestsOrReservations(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	manager := newImportSessionManager(ImportSessionConfig{
		Directory:      filepath.Join(t.TempDir(), "imports"),
		ChunkSizeBytes: 4,
		DiskQuotaBytes: 8,
		MaxSessions:    1,
		TTL:            time.Hour,
		Now:            func() time.Time { return now },
	})
	expired, err := manager.Create(context.Background(), "expired.jsonl", 8, "")
	if err != nil {
		t.Fatalf("create expired session: %v", err)
	}
	cleanupErr := errors.New("filesystem unavailable")
	manager.removeFiles = func(id string, includeMetadata bool) error {
		if id == expired.ID && includeMetadata {
			return cleanupErr
		}
		return nil
	}
	now = now.Add(2 * time.Hour)

	active, err := manager.Create(context.Background(), "active.jsonl", 8, "")
	if err != nil {
		t.Fatalf("cleanup failure blocked replacement session: %v", err)
	}
	if _, err := manager.Get(context.Background(), active.ID); err != nil {
		t.Fatalf("cleanup failure blocked active session: %v", err)
	}
	_, err = manager.Get(context.Background(), expired.ID)
	requireImportSessionErrorCode(t, err, ImportSessionErrorNotFound)
	if _, err := manager.CleanupExpired(context.Background()); !errors.Is(err, cleanupErr) {
		t.Fatalf("explicit cleanup error = %v, want %v", err, cleanupErr)
	}
	if _, ok := manager.cleanupPending[expired.ID]; !ok {
		t.Fatalf("expired session %s was not queued for cleanup retry", expired.ID)
	}

	manager.removeFiles = nil
	removed, err := manager.CleanupExpired(context.Background())
	if err != nil || removed != 0 {
		t.Fatalf("retry cleanup removed = %d error = %v", removed, err)
	}
	if _, ok := manager.cleanupPending[expired.ID]; ok {
		t.Fatalf("expired session %s is still queued after successful retry", expired.ID)
	}
	for _, suffix := range []string{".part", ".json"} {
		if _, statErr := os.Stat(filepath.Join(manager.config.Directory, expired.ID+suffix)); !os.IsNotExist(statErr) {
			t.Fatalf("expired %s still exists after retry: %v", suffix, statErr)
		}
	}
}

type blockingChunkReader struct {
	started   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func newBlockingChunkReader() *blockingChunkReader {
	return &blockingChunkReader{
		started: make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (r *blockingChunkReader) Read([]byte) (int, error) {
	r.startOnce.Do(func() { close(r.started) })
	<-r.closed
	return 0, io.ErrClosedPipe
}

func (r *blockingChunkReader) Close() error {
	r.closeOnce.Do(func() { close(r.closed) })
	return nil
}

func newImportSessionTestService(t *testing.T, sessionConfig ImportSessionConfig) (*Service, context.CancelFunc) {
	t.Helper()
	cfg := testutil.NewConfig(t)
	store := testutil.NewStore(t, cfg)
	service := New(store, WithImportSessions(sessionConfig))
	ctx, cancel := context.WithCancel(context.Background())
	if err := service.StartImportSessionCleanup(ctx); err != nil {
		cancel()
		t.Fatalf("start cleanup: %v", err)
	}
	return service, cancel
}

func waitForImportSessionStatus(
	t *testing.T,
	service *Service,
	id string,
	want ImportSessionStatus,
) ImportSession {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		session, err := service.GetImportSession(context.Background(), id)
		if err == nil && session.Status == want {
			return session
		}
		time.Sleep(10 * time.Millisecond)
	}
	session, err := service.GetImportSession(context.Background(), id)
	t.Fatalf("session status = %#v error = %v, want %s", session, err, want)
	return ImportSession{}
}

func waitForManagerSessionStatus(
	t *testing.T,
	manager *importSessionManager,
	id string,
	want ImportSessionStatus,
) ImportSession {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		session, err := manager.Get(context.Background(), id)
		if err == nil && session.Status == want && (want != ImportSessionStatusCancelled || session.Result != nil) {
			return session
		}
		time.Sleep(10 * time.Millisecond)
	}
	session, err := manager.Get(context.Background(), id)
	t.Fatalf("session status = %#v error = %v, want %s", session, err, want)
	return ImportSession{}
}

func requireImportSessionErrorCode(t *testing.T, err error, want ImportSessionErrorCode) {
	t.Helper()
	var sessionErr *ImportSessionError
	if !errors.As(err, &sessionErr) || sessionErr.Code != want {
		t.Fatalf("error = %v, want code %s", err, want)
	}
}
