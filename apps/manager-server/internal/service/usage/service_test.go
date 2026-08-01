package usage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
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
	_, _ = fmt.Fprintf(
		builder,
		`{"event_hash":%q,"timestamp_ms":%d,"timestamp":"2026-01-02T03:04:05Z","model":"gpt-test","endpoint":"POST /v1/responses"}`+"\n",
		hash,
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
