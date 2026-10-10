package usage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	storepkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
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

func TestImportCanonicalizesNewLegacyEventHashAndDeduplicatesRepeat(t *testing.T) {
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	service := New(st)
	legacyHash := "sk-legacy-event-hash-that-must-not-be-persisted"
	payload := fmt.Sprintf(`{"event_hash":%q,"timestamp_ms":1,"timestamp":"2026-01-01T00:00:00Z","model":"gpt-test"}`+"\n", legacyHash)

	first, _, err := service.Import(context.Background(), strings.NewReader(payload))
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if first.Added != 1 || first.Skipped != 0 {
		t.Fatalf("first result = %#v", first)
	}
	events, err := st.UsageEvents.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	sum := sha256.Sum256([]byte(legacyHash))
	wantHash := hex.EncodeToString(sum[:])
	if events[0].EventHash != wantHash || !usageparser.IsCanonicalSHA256Hex(events[0].EventHash) {
		t.Fatalf("stored event hash = %q, want %q", events[0].EventHash, wantHash)
	}
	if events[0].EventHash == legacyHash {
		t.Fatal("legacy event hash was persisted raw")
	}

	second, _, err := service.Import(context.Background(), strings.NewReader(payload))
	if err != nil {
		t.Fatalf("repeat import: %v", err)
	}
	if second.Added != 0 || second.Skipped != 1 {
		t.Fatalf("repeat result = %#v", second)
	}
	count, _, err := st.Counts(context.Background())
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if count != 1 {
		t.Fatalf("events after repeat = %d, want 1", count)
	}
}

func TestImportRoundTripCanonicalizesLegacyExportWithoutDuplicatingUpgradedDatabase(t *testing.T) {
	ctx := context.Background()
	legacyHash := "legacy/noncanonical/event-hash"
	legacyDB, err := sqliterepo.Open(filepath.Join(t.TempDir(), "legacy.sqlite"))
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	t.Cleanup(func() { _ = legacyDB.Close() })
	if _, err := legacyDB.ExecContext(ctx, `insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, created_at_ms
	) values (?, ?, ?, ?, ?)`, legacyHash, int64(1), "2026-01-01T00:00:00Z", "gpt-test", int64(1)); err != nil {
		t.Fatalf("seed pre-#749 legacy event: %v", err)
	}
	legacyStore := storepkg.New(legacyDB)

	var exported strings.Builder
	if err := legacyStore.WriteExportJSONL(ctx, &exported, 100); err != nil {
		t.Fatalf("export legacy JSONL: %v", err)
	}
	if !strings.Contains(exported.String(), fmt.Sprintf(`"event_hash":%q`, legacyHash)) {
		t.Fatalf("export does not preserve legacy identity: %s", exported.String())
	}

	freshStore := testutil.NewStore(t, testutil.NewConfig(t))
	freshService := New(freshStore)
	first, _, err := freshService.Import(ctx, strings.NewReader(exported.String()))
	if err != nil {
		t.Fatalf("import legacy export into fresh database: %v", err)
	}
	if first.Added != 1 || first.Skipped != 0 {
		t.Fatalf("fresh import result = %#v", first)
	}
	freshEvents, err := freshStore.UsageEvents.ListRecent(ctx, 10)
	if err != nil {
		t.Fatalf("list fresh events: %v", err)
	}
	if len(freshEvents) != 1 || !usageparser.IsCanonicalSHA256Hex(freshEvents[0].EventHash) || freshEvents[0].EventHash == legacyHash {
		t.Fatalf("fresh imported events = %#v", freshEvents)
	}

	repeat, _, err := freshService.Import(ctx, strings.NewReader(exported.String()))
	if err != nil {
		t.Fatalf("repeat fresh import: %v", err)
	}
	if repeat.Added != 0 || repeat.Skipped != 1 {
		t.Fatalf("repeat fresh import result = %#v", repeat)
	}

	upgradedService := New(legacyStore)
	upgraded, _, err := upgradedService.Import(ctx, strings.NewReader(exported.String()))
	if err != nil {
		t.Fatalf("re-import into upgraded legacy database: %v", err)
	}
	if upgraded.Added != 0 || upgraded.Skipped != 1 {
		t.Fatalf("upgraded re-import result = %#v", upgraded)
	}
	var rawCount int
	if err := legacyDB.QueryRowContext(ctx, `select count(*) from usage_events where event_hash = ?`, legacyHash).Scan(&rawCount); err != nil {
		t.Fatalf("count legacy raw identity: %v", err)
	}
	var totalCount int
	if err := legacyDB.QueryRowContext(ctx, `select count(*) from usage_events`).Scan(&totalCount); err != nil {
		t.Fatalf("count upgraded events: %v", err)
	}
	if rawCount != 1 || totalCount != 1 {
		t.Fatalf("upgraded database counts raw=%d total=%d, want 1/1", rawCount, totalCount)
	}
}
