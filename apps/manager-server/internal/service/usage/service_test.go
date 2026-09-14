package usage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
	usageparser "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestImportStreamsBatchesIntoStore(t *testing.T) {
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	var payload strings.Builder
	for index := 0; index < 300; index++ {
		writeImportTestEvent(&payload, fmt.Sprintf("event-%d", index), int64(index+1))
	}
	writeImportTestEvent(&payload, "event-0", 1)

	result, parsed, err := New(st).Import(context.Background(), strings.NewReader(payload.String()))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if parsed == nil || parsed.Total != 301 || result.Total != 301 || result.Added != 300 || result.Skipped != 1 {
		t.Fatalf("result = %#v parsed = %#v", result, parsed)
	}
	events, _, err := st.Counts(context.Background())
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if events != 300 {
		t.Fatalf("events = %d", events)
	}
}

func TestImportNotifiesOnceAfterInsertedEvents(t *testing.T) {
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	service := New(st)
	notifications := 0
	service.SetEventsInsertedNotifier(func() { notifications++ })
	var payload strings.Builder
	for index := 0; index < 300; index++ {
		writeImportTestEvent(&payload, fmt.Sprintf("notify-event-%d", index), int64(index+1))
	}

	result, _, err := service.Import(context.Background(), strings.NewReader(payload.String()))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Added != 300 || notifications != 1 {
		t.Fatalf("result = %#v notifications = %d", result, notifications)
	}
}

func TestImportKeepsCompletedBatchesWhenReaderFails(t *testing.T) {
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	var payload strings.Builder
	for index := 0; index < 300; index++ {
		writeImportTestEvent(&payload, fmt.Sprintf("event-%d", index), int64(index+1))
	}
	readerErr := errors.New("reader failed")
	reader := &errorAtEOFReader{reader: strings.NewReader(payload.String()), err: readerErr}

	_, parsed, err := New(st).Import(context.Background(), reader)
	if !errors.Is(err, readerErr) {
		t.Fatalf("error = %v", err)
	}
	if parsed == nil || parsed.Total != 300 {
		t.Fatalf("parsed = %#v", parsed)
	}
	events, _, countErr := st.Counts(context.Background())
	if countErr != nil {
		t.Fatalf("counts: %v", countErr)
	}
	if events != importBatchSize {
		t.Fatalf("events = %d, want committed batch %d", events, importBatchSize)
	}
}

func TestImportNotifiesAfterPartialSuccess(t *testing.T) {
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	service := New(st)
	notifications := 0
	service.SetEventsInsertedNotifier(func() { notifications++ })
	var payload strings.Builder
	for index := 0; index < 300; index++ {
		writeImportTestEvent(&payload, fmt.Sprintf("partial-notify-event-%d", index), int64(index+1))
	}
	readerErr := errors.New("reader failed")
	reader := &errorAtEOFReader{reader: strings.NewReader(payload.String()), err: readerErr}

	result, _, err := service.Import(context.Background(), reader)
	if !errors.Is(err, readerErr) {
		t.Fatalf("error = %v", err)
	}
	if result.Added != importBatchSize || notifications != 1 {
		t.Fatalf("result = %#v notifications = %d", result, notifications)
	}
}

func writeImportTestEvent(builder *strings.Builder, hash string, timestampMS int64) {
	sum := sha256.Sum256([]byte(hash))
	canonicalHash := hex.EncodeToString(sum[:])
	_, _ = fmt.Fprintf(
		builder,
		`{"event_hash":%q,"timestamp_ms":%d,"timestamp":"2026-01-02T03:04:05Z","model":"gpt-test","endpoint":"POST /v1/responses"}`+"\n",
		canonicalHash,
		timestampMS,
	)
}

type errorAtEOFReader struct {
	reader *strings.Reader
	err    error
}

func (r *errorAtEOFReader) Read(buffer []byte) (int, error) {
	read, err := r.reader.Read(buffer)
	if errors.Is(err, io.EOF) {
		return read, r.err
	}
	return read, err
}

func TestImportRejectsInvalidEventHashWithoutWrappingAsPersistenceError(t *testing.T) {
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	service := New(st)

	invalidPayload := `{"event_hash":"not-a-canonical-hash","timestamp_ms":1,"timestamp":"2026-01-01T00:00:00Z","model":"gpt-test"}` + "\n"

	_, _, err := service.Import(context.Background(), strings.NewReader(invalidPayload))
	if err == nil {
		t.Fatal("expected import error for invalid event hash, got nil")
	}
	if !errors.Is(err, usageparser.ErrInvalidEventHash) {
		t.Fatalf("expected errors.Is(err, usageparser.ErrInvalidEventHash), got: %v", err)
	}
	var persistenceErr *ImportPersistenceError
	if errors.As(err, &persistenceErr) {
		t.Fatalf("expected ErrInvalidEventHash NOT to be wrapped as ImportPersistenceError, got: %v", err)
	}
}
